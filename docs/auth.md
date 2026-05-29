# Authentication

HUE has four principal kinds, split across two credential shapes:

| Principal     | Credential             | Header                        |
|---------------|------------------------|-------------------------------|
| **Owner**     | API key (`own_…`)      | `Authorization: Bearer <key>` |
| **Agent**     | API key (`agt_…`)      | `Authorization: Bearer <key>` |
| **Reseller**  | JWT (via `Login`)      | `Authorization: Bearer <jwt>` |
| **Client**    | JWT (via `Login`)      | `Authorization: Bearer <jwt>` |

The interceptor in
[internal/auth/interceptor.go](../internal/auth/interceptor.go) sniffs
the token shape (starts with `own_`/`agt_` → API key, three
base64url-encoded segments split by `.` → JWT) and dispatches to the
right verifier. The resulting `Actor{Kind, SubjectID, KeyID}` lands
on the request `context.Context`; downstream code reads it via
`auth.FromContext(ctx)`.

After authentication, `AuthorizeUnary` /
`AuthorizeStream` in [internal/server/authz.go](../internal/server/authz.go)
enforces a positive method-allow list: every non-anonymous RPC has an
explicit `(FullMethod → allowed PrincipalKinds)` entry. Mismatch
returns `PermissionDenied`. Anonymous routes (`/healthz`,
`/v1/auth:login`, `/v1/auth:refresh`, gRPC health) live in
`Authenticator.AllowMethods`.

## API keys (Owner + Agent)

Token shape: `<kind>_<base32 body>` where the body is 24 bytes (192
bits) of OS randomness. Total length ≈ 43 chars.

| Field on `api_keys`     | Value |
|-------------------------|-------|
| `kind`                  | `owner` or `agent` |
| `owner_id`              | UUID of the Agent row for AGENT keys; empty for OWNER |
| `prefix`                | `<kind>_` + first 8 chars of body — unique, indexed; the server's lookup key |
| `hash`                  | Argon2id PHC of the **full** token |
| `last_used_at`          | best-effort timestamp |
| `revoked_at` / `expires_at` | non-NULL `revoked_at` or past `expires_at` → 401 |

The plaintext token is **never** stored. `CreateApiKey` returns it
exactly once. Lose it → revoke + reissue.

### Verification flow

```
inbound: Authorization: Bearer <token>
          │
          ▼
   LookupPrefix(token) = "<kind>_<first 8 body chars>"
          │
          ▼
   SELECT * FROM api_keys WHERE prefix = $1   (unique index)
          │
          ▼
   if revoked_at IS NOT NULL or expires_at < now(): 401
          │
          ▼
   argon2id verify (constant-time) full token vs row.hash
          │
          ▼
   ctx = WithActor(ctx, {Kind, SubjectID=owner_id, KeyID})
          │
          ▼
        handler
```

Argon2id is tuned for ~5 ms / verify (memory 16 MiB, time 2, threads
1). The 192-bit body is the primary defense; Argon2id is
defense-in-depth against a DB leak. Comparison uses
[`subtle.ConstantTimeCompare`](https://pkg.go.dev/crypto/subtle#ConstantTimeCompare).

## JWT (Reseller + Client)

Issued by `AuthService.Login`. EdDSA / ed25519 signed.

### Token claims

```json
{
  "iss":  "hue",
  "sub":  "<reseller or client UUID>",
  "kind": "reseller" | "client" | "owner",
  "kid":  "<signing key UUID>",
  "jti":  "<random>",
  "iat":  1715600000,
  "exp":  1715600900,
  "owner_sudo": true
}
```

`owner_sudo` is only present when the token was minted via the Owner
sudo path.

### Token TTLs

| Token   | TTL (defaults from `auth.DefaultAccessTTL` / `DefaultRefreshTTL`) |
|---------|------------------------------------------------------------------|
| access  | 15 minutes |
| refresh | 30 days, opaque base32 (sha256-hashed for DB lookup), rotates on every `Refresh`, old row gets `replaced_by` for reuse detection |

### Signing key

Auto-generated on first boot, persisted to the `signing_keys` table.
The private key is AES-256-GCM encrypted with `HUE_PASSWORD_ENC_KEY`.
Verifier walks every non-revoked `signing_keys` row, so rotation is
non-disruptive: insert a new row, sign with it (`kid` matches new id),
revoke the old when no token still references it.

See `internal/auth/signing_key.go` (`EnsureSigningKey`,
`LoadSigningKeys`).

### Login flow

```
POST /v1/auth:login   {"username": "...", "password": "..."}
   ↓
1. Lockout.IsLocked(ip, username) → 429 RESOURCE_EXHAUSTED if locked
2. Lookup Reseller or Client by username
3. Verify:
     Reseller: Argon2id over `resellers.password_hash`
     Client:   AES-GCM decrypt → constant-time compare
4. Failure → Lockout.RecordFailure; after N failures → emit "login_locked_out" event
   Success → Lockout.Reset, emit "login_succeeded" event
5. Mint access JWT (signed with current SigningKey) + refresh token row
```

Optional Owner-sudo path runs first: if the request *also* presents
an `Authorization: Bearer <own_…>` API key, `password` is ignored, an
`owner_sudo_login` event is emitted, and the JWT carries
`owner_sudo: true`. See [phase2-changes.md → Owner sudo](phase2-changes.md#auth-model-replaces-phase-1-single-shared-key).

### Brute-force lockout

In-memory, per-`(ip, username)`. Default 5 failed attempts → locked
for 15 minutes; configurable via `auth.NewLockout(max, window)`. Each
lockout emits a `login_locked_out` event. The store is process-local
and capped (LRU); restart drops it.

Successful login resets the counter for that `(ip, username)`.

## Refresh / Logout / ChangePassword

| RPC | What it does |
|---|---|
| `Refresh` | Validates the supplied opaque refresh token, mints a new access + refresh pair, marks the old refresh row `replaced_by`. Replay of an already-replaced refresh = token theft signal; the row chain gets revoked. |
| `Logout` | Revokes the supplied refresh token (sets `revoked_at`). Access tokens keep working until their 15-min expiry — that's the blast radius bound. |
| `ChangePassword` | Verifies the old password against the principal's stored credential, then writes the new one (Argon2id for Reseller; AES-GCM-encrypted for Client). Revokes all existing refresh tokens for the principal. |

## Bootstrap

A fresh database has no Owner keys, so nobody can call `CreateApiKey`
to mint the first one. The server breaks the circularity on startup:

1. If `HUE_BOOTSTRAP_TOKEN` is set:
   - Verify the supplied plaintext starts with `own_`.
   - If any non-revoked Owner API key already exists, log and skip
     (idempotent across restarts).
   - Otherwise insert an `api_keys` row with the supplied prefix +
     Argon2id hash.

After first start, **remove** `HUE_BOOTSTRAP_TOKEN` from your secrets
and treat the token like any other Owner key.

Generate a bootstrap token offline:

```go
package main

import (
    "fmt"
    "github.com/hiddify/hue/internal/auth"
)

func main() {
    _, plaintext, _, _ := auth.GenerateKey(auth.KindOwner)
    fmt.Println(plaintext)
}
```

Or in shell:

```bash
echo "own_$(openssl rand -base64 18 | tr -d '/+=' | tr 'A-Z' 'a-z')"
```

## Issuing further API keys

`AuthAdminService.CreateApiKey` (`POST /v1/apiKeys`) — Owner only.
The response carries `token` (plaintext) exactly once.

```bash
curl -sk -H "Authorization: Bearer $OWNER" \
     -H "Content-Type: application/json" \
     https://localhost:8443/v1/apiKeys \
     -d '{"api_key":{
            "kind":"API_KEY_KIND_AGENT",
            "owner_id":"<agent UUID>",
            "name":"fra-1-xray",
            "expires_at":"2027-01-01T00:00:00Z"
          }}'
```

`expires_at` is enforced by the interceptor (past = 401). Use it for
agent keys; humans rotate via Owner sudo + new key.

## Revocation

`POST /v1/apiKeys/{id}:revoke` sets `revoked_at = now()`. The
interceptor reads it on every verify (no cache), so revocation is
effective on the next request.

For JWTs, revocation is **refresh-token rotation only** — short
access TTL (15 min) bounds theft blast radius. Force a full logout by
revoking the principal's refresh tokens then waiting out the access
window.

## Encryption at rest

| Column | Algorithm | Key |
|---|---|---|
| `subscribers.password_ciphertext` | AES-256-GCM | `HUE_PASSWORD_ENC_KEY` |
| `subscribers.private_key_ciphertext` | AES-256-GCM | same |
| `domain_certificates.private_key_ciphertext` | AES-256-GCM | same |
| `signing_keys.private_key_ciphertext` | AES-256-GCM | same |
| `resellers.password_hash` | Argon2id | n/a |
| `api_keys.hash` | Argon2id | n/a |

Wire format for every AES-GCM column: `[key_id (1 byte)] [nonce (12)]
[sealed]`. The `key_id` byte identifies which key in the keyring was
used, enabling zero-downtime key rotation.

## Key rotation (`HUE_ENC_KEYS`)

HUE's encryption layer supports a named keyring so old ciphertexts can
be decrypted with the old key while new ones are encrypted with the
current key.

### Single-key setup (simple)

```bash
# 32 random bytes hex-encoded
HUE_PASSWORD_ENC_KEY=$(openssl rand -hex 32)
```

All rows encrypted with `keyID=1`. Decryption reads `keyID=1` from the
ciphertext header and picks this key.

### Multi-key setup (rotation-ready)

```bash
# Format: "id:hexkey,id:hexkey" — id must be 1–255
HUE_ENC_KEYS="1:$(openssl rand -hex 32),2:$(openssl rand -hex 32)"
HUE_ENC_KEY_CURRENT=2   # new rows use key 2; old rows still decrypt via key 1
HUE_PASSWORD_ENC_KEY=   # leave empty when HUE_ENC_KEYS is set
```

Rules:
- `HUE_ENC_KEYS` takes precedence over `HUE_PASSWORD_ENC_KEY`.
- `HUE_ENC_KEY_CURRENT` must appear in `HUE_ENC_KEYS`.
- Key ID `0` is reserved — means "disabled / passthrough"; never use it
  in a production keyring.
- Key IDs are arbitrary 1-byte integers (1–255); they don't need to be
  sequential.

### Rotation procedure

1. **Add a new key** — append `,3:<hex>` to `HUE_ENC_KEYS` and set
   `HUE_ENC_KEY_CURRENT=3`. Restart HUE. New writes use key 3; old
   rows still decrypt via their stored `keyID`.

2. **Re-encrypt old rows** (optional, background job) — call
   `internal/auth.Reencrypt(ciphertext, oldKeyID)` per row. It
   decrypts with the old key and re-encrypts with the current key. If
   the row is already using the current key it's a no-op.

3. **Retire old key** — once every ciphertext in the DB has been
   re-encrypted, remove key 1 (and 2) from `HUE_ENC_KEYS`. Any attempt
   to decrypt a row still using the old key returns an error rather than
   silently corrupting data.

`internal/auth.Encrypt`, `Decrypt`, and `Reencrypt` are the canonical
API — adapters and server handlers should never call `crypto/aes`
directly.

## mTLS — mutual TLS for agent connections

By default, HUE requires agents to authenticate via API key (Bearer
header). For environments that want **certificate-level** authentication
on top (defense-in-depth, or zero-trust policy enforcement at the TLS
layer), HUE supports mutual TLS.

### Server configuration

```bash
# Path to a PEM CA whose subject keys are allowed as agent clients.
# When set (and HUE_TLS_CERT + HUE_TLS_KEY are also set), HUE requires
# and verifies client certificates on every incoming connection.
HUE_MTLS_CLIENT_CA=/etc/hue/agent-ca.pem
```

The env is read by `hue.Run()` → `auth.LoadMTLSClientCA` →
`auth.ApplyMTLS`. When the CA file is present, the TLS config gains:

```
tls.Config{
    ClientCAs:  <pool from HUE_MTLS_CLIENT_CA>,
    ClientAuth: tls.RequireAndVerifyClientCert,
}
```

HUE logs `mTLS enabled ca=<path>` at startup.

### Agent configuration

Use `agents.AgentDialOption` from `pkg/agents/mtls.go`:

```go
opt, err := agents.AgentDialOption(
    "/etc/agent/client.pem",   // agent certificate (signed by agent CA)
    "/etc/agent/client.key",   // matching private key
    "/etc/hue/server-ca.pem",  // CA that signed HUE's server certificate
)
if err != nil { /* handle */ }

client, err := xray.New(xray.Config{...}, xray.WithDialOption(opt))
```

The agent presents its client cert; HUE verifies it against
`HUE_MTLS_CLIENT_CA`. Both sides verify each other — mutual TLS.

### Certificate hierarchy (recommended)

```
Root CA
├── HUE server cert  (SAN: hue.example.com)
│     issued by: HUE server CA  (can be Root or intermediate)
└── Agent client cert (CN: fra-1-xray)
      issued by: Agent CA  (HUE_MTLS_CLIENT_CA)
```

Separate server CA and agent CA so agent cert compromise doesn't allow
impersonating the server. Use any standard CA toolchain (step-ca,
cfssl, openssl).

### mTLS + API key

mTLS and API key auth are **complementary**, not alternatives. With both
in place:
- The TLS handshake rejects agents without a valid client cert.
- The interceptor verifies the `Authorization: Bearer agt_…` header for
  per-agent identity and permission.

Dropping mTLS doesn't remove the API key check; API key auth alone is
adequate for most deployments.
