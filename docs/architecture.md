# Architecture

How HUE is put together — what each layer owns, where data flows,
which decisions are load-bearing.

## Single-port serving

gRPC and REST share one TLS listener. The handler in
[hue.go](../hue.go) sniffs the request and routes:

```go
if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
    grpcSrv.ServeHTTP(w, r)
    return
}
rootHandler.ServeHTTP(w, r)
```

ALPN handles cleartext-vs-TLS negotiation on the listener; `h2c`
makes HTTP/2 work without TLS in dev. The grpc-gateway router is
wired to dial the gRPC server **in-process via `bufconn`**:

```
HTTP/REST → rootHandler → gwMux → grpc.NewClient(passthrough://buf, bufconn) → gRPC server
                                                                                  ↑
                                                                       same path as
                                                                       native gRPC
```

This means **REST requests pass through the same interceptor chain
as native gRPC**: panic recovery, structured logging, auth, positive
authorization, and protovalidate run uniformly on both. There is no
`cmux`, no second listener, no parallel handler logic.

`rootHandler` mounts these paths before the gateway catch-all:

| Path | Handler |
|---|---|
| `/openapi.json` | augmented OpenAPI spec (adds `bearer` security scheme) |
| `/swagger`, `/swagger/`, `/docs` | embedded Swagger UI HTML |
| `/swagger-ui/` | vendored Swagger UI JS/CSS |
| `/.well-known/acme-challenge/` | `cert.HTTP01Challenger` (for `RequestACME`) |
| `/` | grpc-gateway mux (all `/v1/…` REST routes) |

## Layers

```
                  ┌─────────────────────────────┐
                  │ proto (api/proto/hue/v1/)   │   contract
                  └──────────────┬──────────────┘
                                 │ buf generate
                  ┌──────────────┴──────────────┐
                  │ gen/go/, gen/openapi/       │   codegen
                  └──────────────┬──────────────┘
                                 │ implement
                  ┌──────────────┴──────────────┐
                  │ internal/server/            │   thin shims
                  │   admin.go, auth.go,        │   proto ↔ ent
                  │   certs.go, configsync.go,  │
                  │   resellerclient.go, ...    │
                  └──────────────┬──────────────┘
                                 │ delegate
                  ┌──────────────┴──────────────┐
                  │ internal/service/           │   business logic
                  │   Engine, ResellerHierarchy,│   no proto here
                  │   LockManager, Session,     │
                  │   Penalty                   │
                  └──────────────┬──────────────┘
                                 │ persist
                  ┌──────────────┴──────────────┐
                  │ internal/ent/               │   typed DB client
                  └──────────────┬──────────────┘
                                 │
                          ┌──────┴───────┐
                          │ pgx + Postgres│
                          └──────────────┘
```

Layering is strict in one direction: **service-layer code never
imports proto types**. Translation happens once, in
[internal/server/translate.go](../internal/server/translate.go). This
keeps the engine reusable from tests, benchmarks, and anything
non-gRPC.

A naming twist worth knowing: the ent type for the "tenant who
consumes traffic" is `Subscriber`, but the proto/REST surface calls
it `Client`. ent reserves `Client` as the generated client-type name,
so the schema picked the synonym. The translate layer maps both
ways.

## Principals + auth

Four principal kinds, two credential shapes:

| Principal     | Credential        | How auth runs |
|---------------|-------------------|----------------|
| **Owner**     | API key `own_…`   | Argon2id verify against `api_keys` row |
| **Agent**     | API key `agt_…`   | same |
| **Reseller**  | JWT (`Login`)     | ed25519 verify against any non-revoked `signing_keys` row |
| **Client**    | JWT (`Login`)     | same |

[`internal/auth/interceptor.go`](../internal/auth/interceptor.go)
sniffs the token shape and dispatches. The resulting
`Actor{Kind, SubjectID, KeyID}` lands on the request context;
downstream code reads it via `auth.FromContext(ctx)`.

[`internal/server/authz.go`](../internal/server/authz.go) runs next:
a positive method-allow-list maps `(FullMethod → allowed
PrincipalKinds)`. Mismatch → `PermissionDenied`. The Owner-sudo path
in `AuthService.Login` opportunistically picks up an Owner API key
even when the route is anonymous, accepts any password, and emits
`owner_sudo_login`.

Brute-force lockout (in-memory, per-`(ip, username)`) sits inline
with `Login`. Default 5 attempts / 15 min → `RESOURCE_EXHAUSTED`.

See [auth.md](auth.md) for token formats and verification details.

## The Engine

[`service.Engine`](../internal/service/engine.go) is the orchestrator
for `ReportUsage`. Flow:

1. **Geo + session hash, then drop the IP.** The IP enters
   [`geo.Resolver`](../internal/geo/geo.go) and
   [`SessionTracker.HashIP`](../internal/service/session.go), then is
   set to `""`. Never logged, never persisted.
2. **Penalty short-circuit.** If the Client is in penalty, return
   `ShouldDisconnect` with expiration. No DB work.
3. **Acquire the per-Client lock.**
   [`LockManager`](../internal/service/locks.go) keys a `sync.Map` by
   Client UUID — different Clients don't block each other.
4. **Load Client + active plan** with `WithActivePlan()`.
5. **Apply Node multiplier** to upload+download.
6. **Quota check.** Project new totals; over → mark
   `usage_plans.status = quota_used`, emit `client_suspended`,
   return `ShouldDisconnect`.
7. **Session count.**
   [`SessionTracker.Touch`](../internal/service/session.go) adds the
   IP hash, sweeps expired entries, returns distinct-IP count. Over
   `max_concurrent` → apply penalty, emit `penalty_applied`,
   disconnect.
8. **Reseller hierarchy projection.**
   [`ResellerHierarchy.CheckUsageDelta`](../internal/service/reseller.go)
   walks ancestors, projects aggregate usage, returns the first
   ancestor whose limit would be exceeded.
9. **Single transaction** commits plan counters, Client
   `last_connection_at`, Node counters (with bandwidth check), and
   the ancestor chain.
10. **Emit `usage_recorded` event** — best-effort, after commit.

## Reseller hierarchy enforcement

Resellers form a tree (parent → children); each can carry a
`ResellerPlan` with traffic limits + max-online users. Three rules:

- A child's plan limits **must not exceed** its parent's plan limits.
- A leaf Client's usage **propagates upward** to every ancestor's
  `current_*` counters.
- Any operation that would push an ancestor past its limit is
  **rejected before commit**.

Implementation:
[`internal/service/reseller.go`](../internal/service/reseller.go).
`Ancestors` walks the chain with a 32-level depth bound (cycle
guard). `CheckUsageDelta` projects the candidate write across the
chain. `ApplyUsageDelta` mutates counters inside the open
transaction.

For non-Owner reads/writes, the server layer additionally calls
`DescendantIDs(ctx, callerResellerID)` and intersects the result
with the requested resource's `reseller_id`. This is what makes
`ResellerClientService` and `ResellerManagementService` strictly
descendant-scoped.

## Per-Node config + version selection

`Node.config` is `map<string, google.protobuf.Value>`. Dotted keys:

```
xray.numeric_version.<N>         → JSON value (e.g. full inbound config)
xray.api_endpoint                → "127.0.0.1:10085"
wireguard.numeric_version.<N>    → wg-quick template text
wireguard.interface_address      → "10.0.0.1/24"
```

`<N>` is the **canonical monotone uint64 encoding** of a SemVer-ish
agent version. Produced by
[`pkg/version.Encode`](../pkg/version/version.go):

```
Encode("25.3.7")        = 25_003_007_000
Encode("25.07.01.123")  = 25_007_001_123
Encode("1.8")           =  1_008_000_000
```

When an Agent calls `ConfigService.SyncConfig`:

1. Server resolves `Agent.id` from the API key + checks `Agent.node_id
   == request.node_id` (`PermissionDenied` on mismatch).
2. Computes `encoded = version.Encode(agent_version)`.
3. Walks keys with prefix `<kind>.numeric_version.` and picks the
   largest `N ≤ encoded` (via `version.PickHighestKey`).
4. Returns the chosen value as `config["template"]` plus all
   non-versioned siblings, plus active Clients (with decrypted
   passwords), plus per-Node Certs matching `service_hostnames`.
5. Etag = sha256(updated_at | client_count | cert_count | best_key)
   truncated to 32 chars. Matching `current_etag` → `changed=false`
   and empty payload.

Renderer fans the snapshot into protocol bytes — see
[agents.md](agents.md).

## Event sourcing

Every state change emits an audit event via
[`eventstore.Store.Append`](../internal/eventstore/store.go):

1. Persisted to the `events` table — source of truth.
2. Fanned out to in-process subscribers (registered by
   `StreamEvents`).

Emitted today:
`login_succeeded`, `login_locked_out`, `owner_sudo_login`,
`cert_added`, `usage_recorded`, `client_suspended`,
`node_quota_reached`, `penalty_applied`.

When a subscriber's buffer is full, the event drops *for that
subscriber*, the drop is logged with a counter (REVIEW.md M2),
the rest of fan-out continues.

## Encryption at rest

| Column | Algorithm | Key |
|---|---|---|
| `subscribers.password_ciphertext` | AES-256-GCM | `HUE_PASSWORD_ENC_KEY` |
| `subscribers.private_key_ciphertext` | AES-256-GCM | same |
| `domain_certificates.private_key_ciphertext` | AES-256-GCM | same |
| `signing_keys.private_key_ciphertext` | AES-256-GCM | same |
| `resellers.password_hash` | Argon2id | n/a |
| `api_keys.hash` | Argon2id | n/a |

Wire format for AES-GCM columns: `[key_id(1)] [nonce(12)] [sealed]`.
The `key_id` byte reserves bandwidth for rotation; phase 3 adds a
multi-key registry.

## Privacy: zero raw-IP retention

The IP enters the engine as `ReportInput.ClientIP`, passes once
through `geo.Resolver.Lookup` and once through
`SessionTracker.HashIP`, then `in.ClientIP = ""` is set explicitly.
Nothing downstream sees it. Neither the `events` table nor the
`usage_reports` table has an IP column.

The session salt is **per-process random**, generated once at
startup. Sessions are short-lived relative to process lifetime, so
that's sufficient; the previous "rotate daily" approach allowed
all-day correlation and a discontinuity at midnight.

## Phase-2 risk register (carried forward)

- **Sudo audit trail** lives in the same `events` table. Phase-3
  splits it out to an append-only store.
- **AES-GCM single env key** protects against DB-only compromise,
  not running-process compromise. KMS integration is future work.
- **JWT revocation** is refresh-rotation only. Short access TTL
  (15 min) bounds theft blast radius.
