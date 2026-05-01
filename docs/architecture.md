# Architecture

This document explains *how* HUE is put together — what each layer owns,
where data flows, and which design decisions are load-bearing.

## Single-port serving

gRPC and REST share one TLS listener. The handler in
[cmd/hue/main.go](../cmd/hue/main.go) sniffs the request and routes:

```go
if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
    grpcSrv.ServeHTTP(w, r)
    return
}
gateway.ServeHTTP(w, r)
```

ALPN handles the cleartext-vs-TLS negotiation on the listener; `h2c` makes
HTTP/2 work without TLS in dev. The grpc-gateway router itself is wired
to dial the gRPC server **in-process via `bufconn`**:

```
HTTP/REST → gateway mux → grpc.NewClient(passthrough://buf via bufconn) → gRPC server
                                                                            ↑
                                                                    same path as
                                                                    native gRPC
```

This means **REST requests pass through the same interceptors as native
gRPC**: panic recovery, structured logging, auth, and protovalidate all
run uniformly on both transports. There is no `cmux`, no second listener,
and no parallel handler logic.

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
                     │   admin.go, usage.go, ...   │   proto ↔ ent
                     └──────────────┬──────────────┘
                                    │ delegate
                     ┌──────────────┴──────────────┐
                     │ internal/service/           │   business logic
                     │   Engine, locks, hierarchy  │   no proto here
                     └──────────────┬──────────────┘
                                    │ persist
                     ┌──────────────┴──────────────┐
                     │ internal/ent/               │   typed DB client
                     └──────────────┬──────────────┘
                                    │
                                ┌───┴────┐
                                │ pgx + Postgres│
                                └────────┘
```

The layering is strict in one direction: **service-layer code never
imports proto types**. The translation happens once, in
[internal/server/translate.go](../internal/server/translate.go). This
keeps the engine reusable from tests, benchmarks, and anything else that
isn't gRPC.

## The Engine

[`service.Engine`](../internal/service/engine.go) is the orchestrator for
`ReportUsage`, the system's hot path. The flow is:

1. **Geo + session, then drop the IP.** The client IP enters
   [`geo.Resolver`](../internal/geo/geo.go) and [`SessionTracker.HashIP`](../internal/service/session.go),
   then is set to `""`. It is never logged or persisted.
2. **Penalty short-circuit.** If the user is currently penalized, return
   `ShouldDisconnect` with the expiration time — no DB work.
3. **Acquire the per-user lock.** [`LockManager`](../internal/service/locks.go)
   keys a `sync.Map` by user UUID. Different users do not block each other.
4. **Load user + active plan.** Single query with `WithActivePlan()`.
5. **Apply node multiplier.** `upload, download` are scaled by the node's
   `traffic_multiplier` if known.
6. **Quota check.** Project new totals; if any of `total / upload /
   download` would exceed its limit, mark the user `quota_used`, mark
   the plan `quota_used`, emit `user_suspended`, return `ShouldDisconnect`.
7. **Session count.** [`SessionTracker.Touch`](../internal/service/session.go)
   adds the IP hash, sweeps expired entries, returns the distinct-IP
   count in the configured window. If it exceeds `max_concurrent`, apply
   a penalty and disconnect.
8. **Manager hierarchy projection.** [`ManagerHierarchy.CheckUsageDelta`](../internal/service/manager.go)
   walks ancestors, computes projected aggregated usage, returns the
   first ancestor whose limit would be exceeded.
9. **Single transaction commits everything.** Plan counters, user
   `last_connection_at`, node counters, service counters, and the manager
   ancestor chain all advance in one ent `Tx`.
10. **Emit `usage_recorded` event** — best-effort, after commit.

This satisfies REVIEW.md H2 (short critical sections) and H3 (single tx
for batched writes) from the audit of the previous implementation.

## Manager hierarchy enforcement

Managers form a tree (parent → children) and each can have a `ManagerPlan`
with traffic limits, max-online-users, etc. Three rules apply:

- A child's plan limits **must not exceed** its parent's plan limits.
- A leaf user's usage **propagates upward** to every ancestor's
  `current_*` counters.
- Any operation that would push an ancestor's projected usage past its
  limit is **rejected before the transaction commits**.

The implementation lives in
[`internal/service/manager.go`](../internal/service/manager.go). `Ancestors`
walks the chain (with a depth bound to detect cycles), `CheckUsageDelta`
projects the candidate write across all ancestors, and `ApplyUsageDelta`
mutates ancestor counters inside an open transaction.

## Authentication

Per-actor API keys, Argon2id-hashed at rest. See [auth.md](auth.md) for the
token format and verification flow. The interceptor in
[`internal/auth/interceptor.go`](../internal/auth/interceptor.go) attaches
an `Actor{Kind, OwnerID, KeyID}` to the request `context.Context`; service
code reads it via `auth.FromContext(ctx)` to enforce per-actor scoping.

## Event sourcing

Every state change emits an audit event via
[`eventstore.Store.Append`](../internal/eventstore/store.go). The event is:

1. Persisted to the `events` table — that's the source of truth.
2. Fanned out to in-process subscribers (registered by `StreamEvents`).

When a subscriber's buffer is full, the event is dropped for that
subscriber, the drop is **logged with a counter** (REVIEW.md M2 fixed the
silent-drop bug), and the rest of the fan-out continues unaffected.

## Privacy: zero raw-IP retention

The IP enters the engine in `ReportInput.ClientIP`, gets passed once to
[`geo.Resolver.Lookup`](../internal/geo/geo.go) (which doesn't log it
either) and once to [`SessionTracker.HashIP`](../internal/service/session.go),
then `in.ClientIP = ""` is set explicitly. Nothing downstream sees it.
Neither the `events` table nor the `usage_reports` table has an IP column.

The session salt is **per-process random**, generated once at startup. The
previous implementation rotated it daily, which (a) allowed correlation
across the whole calendar day and (b) created a discontinuity in session
counts at midnight. Process-lifetime random is sufficient because
sessions are short-lived relative to process lifetime.
