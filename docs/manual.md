# How-To Manual

This is the working operator's manual: how to actually *use* a running
HUE deployment from "I have a bootstrap token" through "my nodes are
reporting usage and I'm provisioning customers".

For installation, see [deployment.md](deployment.md). For the formal API
reference, see [api.md](api.md).

Throughout this guide:

- `$TOKEN` is your bootstrap manager API key (printed by the server on
  first start; see [auth.md](auth.md) if you don't have one).
- `$BASE` is the public URL: `https://hue.example.com:8443`.
- All examples show **REST first** (curl) and the **native gRPC
  equivalent** (grpcurl) right under it. Both hit the same code path.

---

## Easiest way to explore: open Swagger UI

Every HUE instance serves an interactive API explorer at
`https://your-host:8443/swagger/`. Click **Authorize**, paste
`Bearer <your-token>`, then click **Try it out** on any RPC to call it
without leaving the browser. The spec lives at `/openapi.json`; both
endpoints are unauthenticated (they expose schema only, never data).

The rest of this manual uses curl + grpcurl so you can copy-paste into
scripts, but the Swagger UI is often faster for one-off pokes.

## Step 0 — Sanity check

Confirm the server is reachable and your token works:

```bash
# REST
curl -sk "$BASE/healthz"
# {"status":"HEALTH_STATUS_SERVING"}

curl -sk -H "Authorization: Bearer $TOKEN" "$BASE/v1/users?page_size=1"
# {} on a fresh DB

# gRPC
grpcurl -insecure -H "authorization: Bearer $TOKEN" \
  -d '{"page_size":1}' "$BASE_HOST" hue.v1.AdminService/ListUsers
```

If the second call returns 401, your bootstrap token doesn't match what
the server has stored. See [auth.md → Bootstrap](auth.md#bootstrap).

---

## Step 1 — Decide your tenant model

HUE has three actor kinds and one tree of *managers*. Pick a shape
before you create anything; it's much harder to restructure later.

```
                ┌────────────┐
                │   root     │  ← bootstrapped by HUE_BOOTSTRAP_TOKEN
                └──────┬─────┘
            ┌──────────┴──────────┐
            ▼                     ▼
        ┌──────┐              ┌──────┐
        │  EU  │              │  US  │      ← regional resellers
        └───┬──┘              └───┬──┘
        ┌───┴────┐            ┌───┴────┐
        ▼        ▼            ▼        ▼
       ACME    GLOBEX        ZORG     ...   ← end-customer accounts
      (users) (users)       (users)
```

- The **root manager** owns everything. Bootstrapped automatically.
- **Resellers** can have their own resellers under them. There's no hard
  depth limit; the cycle-detector trips at 32 levels.
- **Users** belong to exactly one manager (their direct parent).
- **Services** and **nodes** are global — they're hardware/transport, not
  tenants.

---

## Step 2 — Create your manager hierarchy

```bash
# REST
curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/managers" \
  -d '{"name":"EU","status":"MANAGER_STATUS_ACTIVE"}'

# Response includes the new manager's id; capture it:
EU_ID=$(curl -sk -H "Authorization: Bearer $TOKEN" \
  "$BASE/v1/managers" | jq -r '.managers[] | select(.name=="EU").id')

# Create a child of EU
curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/managers" \
  -d "{\"name\":\"ACME\",\"parentId\":\"$EU_ID\"}"
```

```bash
# gRPC equivalent
grpcurl -insecure -H "authorization: Bearer $TOKEN" \
  -d "{\"manager\":{\"name\":\"EU\"}}" \
  "$BASE_HOST" hue.v1.AdminService/CreateManager
```

---

## Step 3 — Issue per-actor API keys

The bootstrap token is a manager key for the root manager; you
shouldn't hand it out. Issue narrower keys for everyone else:

```bash
# A key for the EU reseller (so they can create their own users)
curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/apiKeys" \
  -d "{
    \"kind\":\"API_KEY_KIND_MANAGER\",
    \"ownerId\":\"$EU_ID\",
    \"name\":\"eu-admin\"
  }"
```

The response includes `token` — the **plaintext only ever returned
here**. Save it; if lost, revoke and reissue.

```json
{
  "apiKey": { "id": "...", "kind": "...", "prefix": "mgr_3xqq...", "lifecycle": {...} },
  "token":  "mgr_3xqq74dk5twh..."
}
```

The same flow works for `API_KEY_KIND_SERVICE` (issue to a service row's
id) and `API_KEY_KIND_NODE` (issue to a node row's id).

---

## Step 4 — Onboard a node

A *node* is a logical group of services on one host (typically a VPN
server).

```bash
curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/nodes" \
  -d '{
    "name":              "fra-1",
    "ips":               ["198.51.100.10"],
    "trafficMultiplier": 1.0,
    "geo":               {"country":"DE","city":"Frankfurt"}
  }'
```

`trafficMultiplier` lets you charge premium nodes at e.g. `1.5x`
counted bytes — useful for high-cost transit.

Now mint a node API key for that host so it can call `UsageService`:

```bash
NODE_ID="..."   # from the response above
curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/apiKeys" \
  -d "{\"kind\":\"API_KEY_KIND_NODE\",\"ownerId\":\"$NODE_ID\",\"name\":\"fra-1\"}"
```

Drop the returned `token` into the node's secret store. The host
authenticates as that node from now on.

---

## Step 5 — Define services on the node

A *service* is a specific protocol on the node — e.g. vless on port
443, wireguard on 51820. Each service gets its own API key so a
compromised vless config doesn't grant access to wireguard's view.

```bash
curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/services" \
  -d "{
    \"nodeId\":   \"$NODE_ID\",
    \"name\":     \"vless-fra\",
    \"protocol\": \"PROTOCOL_VLESS\",
    \"allowedAuthMethods\": [\"AUTH_METHOD_UUID\",\"AUTH_METHOD_PASSWORD\"]
  }"
```

The response is `{ "service": {...}, "apiKey": "svc_..." }` — the
plaintext key is shown **once**.

---

## Step 6 — Create a user with a usage plan

```bash
# Create the user
curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/users" \
  -d "{
    \"info\":       {\"groups\":[\"premium\"],\"managerId\":\"$EU_ID\"},
    \"authMethod\": {\"username\":\"alice\",\"password\":\"strongpass\"}
  }"

USER_ID=$(curl -sk -H "Authorization: Bearer $TOKEN" \
  "$BASE/v1/users?search=alice" | jq -r '.users[0].info.id')
```

User creation does NOT auto-assign a usage plan; you create one
separately so the same user can have a history of plans. (The plan
endpoints — `CreateUsagePlan`, `ResetUserUsage` — are on the roadmap;
for now, plans get created via direct DB or backfilled via the engine
when implemented.)

---

## Step 7 — Report usage from a node

This is the data plane. Nodes call `UsageService.ReportUsage` (or
`BatchReportUsage` for high volume) with their node-API-key.

```bash
# Use the NODE token from step 4, not the bootstrap token
curl -sk -H "Authorization: Bearer $NODE_TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/usage:report" \
  -d "{
    \"userId\":    \"$USER_ID\",
    \"nodeId\":    \"$NODE_ID\",
    \"serviceId\": \"$SVC_ID\",
    \"upload\":    1048576,
    \"download\":  4194304,
    \"sessionId\": \"sess-xyz\",
    \"clientIp\":  \"203.0.113.5\"
  }"
```

The response is the engine's decision:

```json
{
  "decision": {
    "accepted":         true,
    "quotaExceeded":    false,
    "sessionLimitHit":  false,
    "shouldDisconnect": false
  }
}
```

If `shouldDisconnect: true`, the node should drop the user's session.
The `clientIp` is **never persisted or logged** — the engine extracts
geo metadata + a session hash, then drops the string.

For high throughput, batch:

```bash
curl -sk -H "Authorization: Bearer $NODE_TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/usage:batchReport" \
  -d '{"reports":[ {...}, {...}, ... up to 1000 ... ]}'
```

---

## Step 8 — See what's happening

### Current users + their state

```bash
curl -sk -H "Authorization: Bearer $TOKEN" \
  "$BASE/v1/users?status=USER_STATUS_ACTIVE&page_size=50"
```

### One user's plan + counters

```bash
curl -sk -H "Authorization: Bearer $TOKEN" \
  "$BASE/v1/users/$USER_ID/activeUsagePlan"
```

(Endpoint stubbed today; tracked under [operations.md → Known-unimplemented RPCs](operations.md#known-unimplemented-rpcs).)

### Recent events

```bash
curl -sk -H "Authorization: Bearer $TOKEN" \
  "$BASE/v1/events?type=EVENT_TYPE_USAGE_RECORDED&page_size=100"
```

### Live event stream

```bash
# Server-streaming over chunked HTTP
curl -sk -H "Authorization: Bearer $TOKEN" -N \
  "$BASE/v1/events:stream?types=EVENT_TYPE_USER_SUSPENDED"

# Or via gRPC for proper backpressure semantics
grpcurl -insecure -H "authorization: Bearer $TOKEN" \
  -d '{"types":["EVENT_TYPE_USER_SUSPENDED"]}' \
  "$BASE_HOST" hue.v1.AdminService/StreamEvents
```

The stream applies a small per-subscriber buffer; if your consumer
falls behind, drops are logged with a counter (see [operations.md
→ A subscriber is dropping events](operations.md#a-subscriber-is-dropping-events)).

---

## Step 9 — Day-to-day operations

### Suspend a misbehaving user

```bash
curl -sk -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/users/$USER_ID" \
  -d '{"info":{"status":"USER_STATUS_SUSPENDED"}}'
```

The next `ReportUsage` call for that user returns
`shouldDisconnect: true` and the node drops the session.

### Revoke an API key

```bash
curl -sk -X POST -H "Authorization: Bearer $TOKEN" \
  "$BASE/v1/apiKeys/$KEY_ID:revoke"
```

Effect is immediate — the gRPC interceptor checks `revoked_at` on every
verify.

### List all keys for an owner

```bash
curl -sk -H "Authorization: Bearer $TOKEN" \
  "$BASE/v1/apiKeys?ownerId=$NODE_ID&kind=API_KEY_KIND_NODE"
```

### Reset a user's usage at billing time

`POST /v1/users/$USER_ID:resetUsage` — currently stubbed, scheduled. In
the meantime, do it directly via SQL inside a maintenance window:

```sql
BEGIN;
UPDATE usage_plans
   SET current_total = 0, current_upload = 0, current_download = 0
 WHERE user_id = $1 AND status = 'active';
COMMIT;
```

Don't forget to also bump the `expires_at` if you're rolling the
billing period.

### Move a user to a different reseller

```bash
curl -sk -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/users/$USER_ID" \
  -d "{\"info\":{\"managerId\":\"$NEW_MANAGER_ID\"}}"
```

Future usage goes to the new manager's chain. **Past usage already
aggregated to the old chain stays there** — that's by design (audit
trail). If you need clean reassignment, reset the user's plan first.

---

## End-to-end recipe: "Set up a 100-user reseller in 1 minute"

```bash
set -euo pipefail

# 1. Reseller
RES_ID=$(curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/managers" \
  -d '{"name":"acme-corp"}' | jq -r .id)

# 2. Reseller's admin key (they manage their own users with this)
RES_TOKEN=$(curl -sk -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/apiKeys" \
  -d "{\"kind\":\"API_KEY_KIND_MANAGER\",\"ownerId\":\"$RES_ID\",\"name\":\"acme-admin\"}" \
  | jq -r .token)

# 3. 100 users — all owned by the reseller, all with username userXXX
for i in $(seq 1 100); do
  curl -sk -H "Authorization: Bearer $RES_TOKEN" -H 'Content-Type: application/json' \
    "$BASE/v1/users" \
    -d "{
      \"info\":       {\"managerId\":\"$RES_ID\",\"groups\":[\"acme\"]},
      \"authMethod\": {\"username\":\"acme-user-$i\",\"password\":\"$(openssl rand -hex 12)\"}
    }" > /dev/null
done

# 4. Verify
curl -sk -H "Authorization: Bearer $RES_TOKEN" \
  "$BASE/v1/users?page_size=10" | jq '.total'
# 100
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `401 missing Authorization header` | Forgot the `Authorization: Bearer ...` header | Add it |
| `401 unknown token` | Token is well-formed but not in DB. Either it was revoked or you're hitting a different deployment than where it was minted. | Verify `LookupPrefix(token)` against `SELECT prefix FROM api_keys` on the deployment you're talking to. |
| `400 unknown field "user"` on `POST /v1/users` | You wrapped the body as `{"user":{...}}`. The `body: "user"` annotation in the proto means the body **is** the User. | Send the User object directly. |
| `ReportUsage` always returns `"no active plan"` | User has no row in `usage_plans` with the user's id and `active_plan_id` set on the user. | Create a plan and set `users.active_plan_id`. The CreateUsagePlan RPC is on the roadmap; until then, do it in SQL or via direct ent. |
| Disconnect storms after a brief outage | Many users tripped `max_concurrent` because their clients reconnected from new IPs faster than the window cleared. | Bump `HUE_CONCURRENT_WINDOW` (default 5 min) or `max_concurrent` per user. |
| `409 AlreadyExists` on `CreateUser` | Username is unique; collision. | Pick a different username (or include the manager id as a prefix in your naming convention). |
| Gateway returns 200 with body `{}` | Successful call but proto3 JSON omits empty fields by default. | That's correct behavior — if `users: []` and `total: 0`, both are dropped. Check the actual response shape; an empty `{}` means "no results". |
| Counters don't update after a `ReportUsage` | The decision was rejected — check `accepted` / `reason` in the response. | If `accepted: false`, the engine is telling you the report was *not* applied. |

---

## Where the formal docs live

- **API surface**: [api.md](api.md) — every RPC, every REST route, every error code.
- **Auth deep dive**: [auth.md](auth.md) — token format, Argon2id parameters, bootstrap.
- **Configuration**: [configuration.md](configuration.md) — every `HUE_*` env var.
- **Architecture**: [architecture.md](architecture.md) — single-port handler, layer boundaries, manager hierarchy enforcement, the engine's 10-step ReportUsage flow.
- **Operations**: [operations.md](operations.md) — health probes, observability, graceful shutdown, runbook.
