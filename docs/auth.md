# Authentication

HUE issues per-actor API keys. There are three actor kinds:

| Kind | Token prefix | Owner |
|---|---|---|
| Manager | `mgr_` | a `manager` row (humans, resellers, admin tooling) |
| Service | `svc_` | a `service` row (a specific protocol on a specific node) |
| Node | `nod_` | a `node` row (a host) |

A token is `<kind_prefix>_<base32 body>`, where the body is 24 bytes
(192 bits) of OS randomness. Total token length is around 43 chars.

## What gets stored

For each issued token, the database row in `api_keys` carries:

- `kind` — manager / service / node.
- `owner_id` — the manager/service/node UUID this key authenticates as.
- `prefix` — `<kind>_` plus the first 8 chars of the body. Indexed,
  unique. The server uses this as its lookup key.
- `hash` — the **Argon2id PHC string** of the full token. Includes
  parameters and salt; verification re-runs Argon2id and constant-time
  compares.
- `last_used_at` — best-effort timestamp updated by a goroutine after
  each successful auth. Failures don't reach the user.
- `revoked_at` — non-NULL means the key is rejected by the interceptor.

The plaintext token is **never** stored. It is shown to the caller of
`CreateApiKey` exactly once, in the response. Lose it, and you have to
revoke + reissue.

## Verification flow

```
inbound request with Authorization: Bearer <token>
              │
              ▼
   parse "Bearer ", split <kind>_<body> on first _
              │
              ▼
   compute LookupPrefix(token) = <kind>_<first 8 body chars>
              │
              ▼
   SELECT * FROM api_keys WHERE prefix = $1   (unique index)
              │
              ▼
   if revoked_at IS NOT NULL: 401
              │
              ▼
   argon2id verify (constant time) over full token vs row.hash
              │
              ▼
   ctx = WithActor(ctx, Actor{Kind, OwnerID, KeyID})
              │
              ▼
            handler
```

The Argon2id parameters are tuned for ~5ms per verify on a modern CPU
(memory=16 MiB, time=2, threads=1). Tokens already carry 192 bits of
entropy so the slowdown is defense-in-depth against database leaks, not
the primary security control.

The comparison uses [`subtle.ConstantTimeCompare`](https://pkg.go.dev/crypto/subtle#ConstantTimeCompare).

## Bootstrap

A fresh database has no manager keys, so no one can call
`CreateApiKey` to mint the first one. The server breaks the circularity
on startup:

1. If `HUE_BOOTSTRAP_TOKEN` is set:
   - Verify the supplied plaintext starts with `mgr_`.
   - If any non-revoked manager API key already exists, log and skip
     (idempotent across restarts).
   - Otherwise, find or create a `Manager` named `root`, then insert an
     `api_keys` row with the supplied prefix + Argon2id hash.

After first start, **remove** `HUE_BOOTSTRAP_TOKEN` from your secrets and
treat it as a regular API key.

To generate a bootstrap token offline:

```go
package main

import (
	"fmt"
	"github.com/hiddify/hue/internal/auth"
)

func main() {
	_, plaintext, _, _ := auth.GenerateKey(auth.KindManager)
	fmt.Println(plaintext)
}
```

Or in shell, since the format is just a kind prefix + 24 random bytes
base32-encoded lowercase:

```bash
echo "mgr_$(openssl rand -base64 18 | tr -d '/+=' | tr 'A-Z' 'a-z')"
```

## Issuing further keys

Use `AdminService.CreateApiKey` (`POST /v1/apiKeys`) with the bootstrap
token in `Authorization`. The response carries `token` — the plaintext —
exactly once.

```bash
curl -sk -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     https://localhost:8443/v1/apiKeys \
     -d '{
       "api_key": {
         "kind":     "API_KEY_KIND_SERVICE",
         "owner_id": "<service UUID>",
         "name":     "vless-eu-prod"
       }
     }'
```

## Revocation

`POST /v1/apiKeys/{id}:revoke` sets `revoked_at = now()`. The interceptor
rejects any subsequent request with that token. Revocation is immediate;
there is no token cache.

## Per-actor authorization

The interceptor attaches the authenticated `Actor{Kind, OwnerID}` to the
`context.Context`. Service-layer code uses
[`auth.FromContext(ctx)`](../internal/auth/context.go) to enforce
per-actor scope (e.g. "managers can only modify users they own"). The
scoping rules are not yet wired uniformly across every RPC — track it as
a follow-up. Until they are, treat manager keys as effectively
admin-level and don't issue them broadly.
