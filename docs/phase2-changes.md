# Phase 2 — what changed since phase 1

A working reference for anyone reading the older docs alongside the
current code. The phase-1 docs (manual.md, api.md, auth.md, …) still
describe accurate concepts but use the old vocabulary in places.

## Vocabulary

| Old term (phase 1)     | New term (phase 2)             | What it is now                                                           |
|------------------------|--------------------------------|--------------------------------------------------------------------------|
| `User`                 | **Client** (proto/REST/UI)     | Tenant that consumes traffic. Has username + plaintext-equivalent pw.    |
| `User`                 | **Subscriber** (Go ent type)   | Same row; ent can't name a schema `Client` because `*ent.Client` reserves it. Translation in `internal/server/translate.go`. |
| `Manager`              | **Reseller**                   | Tree of human admins. Argon2id-hashed password. JWT via `AuthService.Login`. |
| `ManagerPlan`          | **ResellerPlan**               | Unchanged shape, renamed.                                                |
| `Service`              | **Agent**                      | Process on a Node (xray, wireguard, …) that consumes HUE's API.          |
| n/a                    | **Owner**                      | Singleton root principal. Owner = API key only. Can sudo as any Reseller/Client via `Login(user, "anypass")`. Implicit parent of every Reseller. |
| Per-service config     | **Per-Node config**            | `Node.config` is `map<string, structpb.Value>` with dotted keys like `xray.numeric_version.<N>`. |

## Service split

`AdminService` in phase 1 owned all CRUD. Phase 2 splits along blast radius:

| Service | Principals | Surface |
|---|---|---|
| **AuthService**              | anonymous     | Login / Refresh / Logout / ChangePassword |
| **AdminService**             | Owner         | Node + Agent CRUD + Events + Health |
| **AuthAdminService**         | Owner         | API key CRUD |
| **ResellerClientService**    | Reseller, Owner | Client CRUD + UsagePlan reads + GetAvailableNodesInfo |
| **ResellerManagementService**| Reseller, Owner | Sub-Reseller CRUD (recursive descendants only) |
| **ConfigService**            | Agent         | Per-Node SyncConfig + Heartbeat |
| **DomainCertificateService** | Owner writes, Reseller/Owner reads | BYO cert store + (ACME pending) |
| **UsageService**             | Agent         | ReportUsage + BatchReportUsage + SyncClients |

Positive authorization runs in
[`internal/server/authz.go`](../internal/server/authz.go): every
non-anonymous RPC has an explicit `(method → allowed PrincipalKinds)`
entry; mismatch returns `PermissionDenied`. Anonymous routes go
through `Authenticator.AllowMethods` instead.

## Auth model (replaces phase-1 single shared key)

| Role          | Credential                  | Header                        |
|---------------|-----------------------------|-------------------------------|
| **Owner**     | API key (`own_<base32>`)    | `Authorization: Bearer …`     |
| **Agent**     | API key (`agt_<base32>`)    | `Authorization: Bearer …`     |
| **Reseller**  | JWT via `AuthService.Login` | `Authorization: Bearer <jwt>` |
| **Subscriber**| JWT via `AuthService.Login` | `Authorization: Bearer <jwt>` |

Wire-format details:

- API keys: 192-bit body, Argon2id-hashed at rest, lookup-prefix
  indexed.
- JWTs: EdDSA-signed (ed25519). HUE auto-generates a signing key on
  first boot and persists it to the `signing_keys` table; rotation
  works without invalidating in-flight tokens (verifier walks
  non-revoked rows).
- Refresh tokens: opaque base32, sha256-hashed for DB lookup,
  rotated on every `Refresh`, old row gets `replaced_by` for reuse
  detection.
- Brute-force lockout: in-memory per-`(ip, username)`, default
  5 attempts / 15 min window → `RESOURCE_EXHAUSTED`. Configurable
  via `auth.NewLockout(maxAttempts, window)`.
- Owner sudo: `Login(user, "anypass")` with an Owner API key accepts
  any password and mints a JWT for the target Reseller/Subscriber.
  Every sudo writes an `OWNER_SUDO_LOGIN` event with the owner's
  `key_id` + target id.

## Encryption at rest

| Column                                  | Algorithm    | Key                          |
|-----------------------------------------|--------------|------------------------------|
| `subscribers.password_ciphertext`       | AES-256-GCM  | `HUE_PASSWORD_ENC_KEY` (env) |
| `subscribers.private_key_ciphertext`    | AES-256-GCM  | same                         |
| `domain_certificates.private_key_ciphertext` | AES-256-GCM | same                       |
| `signing_keys.private_key_ciphertext`   | AES-256-GCM  | same                         |
| `resellers.password_hash`               | Argon2id     | n/a (one-way)                |
| `api_keys.hash`                         | Argon2id     | n/a (one-way)                |

Wire format on every AES-GCM ciphertext: `[keyID(1) | nonce(12) |
sealed]`. The `key_id` byte enables future rotation (Phase 3) without
invalidating existing rows.

Tests intentionally set `HUE_PASSWORD_ENC_KEY` so the encryption path
exercises in CI rather than the pass-through fallback used when the env
var is empty.

## ConfigService — per-Node config + version selection

`Node.config` is `map<string, google.protobuf.Value>`. Keys are dotted;
the convention is:

```
xray.numeric_version.<N>     → JSON value (e.g. a full inbound config)
xray.api_endpoint            → "127.0.0.1:10085"
wireguard.numeric_version.<N>→ wg-quick template text
wireguard.interface_address  → "10.0.0.1/24"
```

`<N>` is the **canonical monotone uint64 encoding** of a SemVer-ish
agent version, produced by
[`pkg/version.Encode`](../pkg/version/version.go):

```
Encode("25.3.7")        = 25_003_007_000
Encode("25.07.01.123")  = 25_007_001_123
Encode("1.8")           = 1_008_000_000
```

When an Agent calls `SyncConfig(node_id, agent_kind, agent_version,
current_etag)`:

1. Server resolves `Agent.id` from the API key and checks
   `Agent.node_id == request.node_id` (PermissionDenied otherwise).
2. Computes `encodedAgent = version.Encode(agent_version)`.
3. Walks `Node.config` keys with prefix `<kind>.numeric_version.` and
   picks the largest `N ≤ encodedAgent` (via `version.PickHighestKey`).
4. Returns the chosen value as `config["template"]`, plus all
   non-versioned siblings under the same root (`xray.api_endpoint`, …),
   plus active Clients (with decrypted passwords + public keys) +
   per-Node Certs.
5. Etag = sha256(updated_at | client_count | cert_count | best_key)
   truncated to 32 chars. Matching `current_etag` → `changed=false` and
   empty payload.

The Agent's renderer fans those out into protocol-specific bytes — see
[agents.md](agents.md).

## DomainCertificateService

New, phase-2. See [certificates.md](certificates.md).

- `AddCertificate` validates PEM + expiry + domain coverage, then
  AES-GCM-encrypts the private key.
- `GetCertificate` matches by domain (exact, wildcard, IP).
- `ListCertificates` redacts private keys unless the caller is Owner.
- `RequestACME` drives a single Let's Encrypt issuance over HTTP-01
  via `lego/v4`. HUE mounts the challenge handler at
  `/.well-known/acme-challenge/` on its own listener. Returns
  `Unimplemented` until `HUE_ACME_CONTACT_EMAIL` is set.
- Self-signed generator (`internal/cert.SelfSigned`) ships today; used
  as fallback when `HUE_SECURE=true` is set with no usable cert.

## What's stubbed / deferred

- **Real xray E2E with binary download + speedtest** (phase 2.7b)
- **AdminService.StreamEvents / ConfigService disconnect_client_ids hints** — phase-2 stubs return empty

## Migration

Phase 2 was developed against a throwaway dev DB. There is no
phase-1 → phase-2 migration script — drop the existing tables and let
`Schema.Create` rebuild. Production migration scripts can be generated
via Atlas when a real rollout approaches.
