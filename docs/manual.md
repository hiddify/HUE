# How-To Manual

The working operator's manual: from "I have a bootstrap token" to
"my nodes are reporting usage and my resellers are provisioning
clients". For installation, see [deployment.md](deployment.md). For
the formal API contract, see [api.md](api.md).

Throughout this guide:

- `$OWNER` is the Owner API key minted from `HUE_BOOTSTRAP_TOKEN`
  (printed on first start; see [auth.md → Bootstrap](auth.md#bootstrap)).
- `$BASE` is the public URL: `https://hue.example.com:8443`.
- `$BASE_HOST` is the host:port (`hue.example.com:8443`).
- Examples show **REST first** (curl) and the **native gRPC
  equivalent** (grpcurl) right under it. Both hit the same code path.

---

## Easiest way to explore: Swagger UI

Every HUE instance serves an interactive API explorer at
`$BASE/swagger/`. Click **Authorize**, paste `Bearer <your-token>`,
then "Try it out" any RPC without leaving the browser. The spec lives
at `/openapi.json`; both endpoints are unauthenticated (schema only).

## Step 0 — Sanity check

```bash
# REST
curl -sk "$BASE/healthz"
# {"status":"HEALTH_STATUS_SERVING"}

curl -sk -H "Authorization: Bearer $OWNER" "$BASE/v1/clients?page_size=1"
# {} on a fresh DB

# gRPC
grpcurl -insecure -H "authorization: Bearer $OWNER" \
  -d '{"page_size":1}' "$BASE_HOST" hue.v1.ResellerClientService/ListClients
```

A 401 means the bootstrap token doesn't match what the server has
stored — see [auth.md → Bootstrap](auth.md#bootstrap).

---

## Step 1 — Pick your tenant shape

HUE has four principals; pick a shape before you create anything.

```
       ┌──────────────┐
       │   Owner      │  ← bootstrap-minted; ROOT, sudo, audit-logged
       └──────┬───────┘
              │
   ┌──────────┴───────────┐
   ▼                      ▼
┌────────┐           ┌────────┐
│  EU    │           │  US    │   ← Resellers
└───┬────┘           └───┬────┘
    │                    │
 ┌──┴──┐              ┌──┴──┐
 ▼     ▼              ▼     ▼
ACME  GLOBEX         ZORG  …       ← sub-Resellers
 │    │
 ▼    ▼
Clients (tenants who consume traffic)

   ─── separate layer ───

┌──────────┐    ┌──────────┐
│ Node fra-1│   │ Node nyc-1│   ← Nodes (hosts)
└────┬──────┘   └─────┬─────┘
     │                │
   Agents (xray, wireguard, …)
```

- **Owner** is API-key only. Hand the Owner key only to root
  operators / automation that must sudo.
- **Resellers** have a tree; non-Owner principals only see their
  descendants. Cycle-detector trips at 32 levels deep.
- **Clients** consume traffic; each owned by exactly one Reseller.
- **Nodes** and **Agents** are global infrastructure — orthogonal to
  the Reseller tree.

---

## Step 2 — Create the Reseller tree

Owner uses `ResellerManagementService`. Each Reseller gets a password
(Argon2id-hashed at rest) so the human logs in via `AuthService.Login`.

```bash
# Create EU regional reseller
curl -sk -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  "$BASE/v1/resellers" \
  -d '{"reseller":{"name":"EU","password":"euAdminPw!","status":"RESELLER_STATUS_ACTIVE"}}'

# Capture EU's id
EU_ID=$(curl -sk -H "Authorization: Bearer $OWNER" \
  "$BASE/v1/resellers" | jq -r '.resellers[] | select(.name=="EU").id')

# Create ACME under EU
curl -sk -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  "$BASE/v1/resellers" \
  -d "{\"reseller\":{\"name\":\"ACME\",\"parent_id\":\"$EU_ID\",\"password\":\"acmePw!\"}}"
```

```bash
# gRPC equivalent
grpcurl -insecure -H "authorization: Bearer $OWNER" \
  -d "{\"reseller\":{\"name\":\"EU\",\"password\":\"euAdminPw!\"}}" \
  "$BASE_HOST" hue.v1.ResellerManagementService/CreateReseller
```

EU's admin can now log in:

```bash
EU_JWT=$(curl -sk "$BASE/v1/auth:login" -H 'Content-Type: application/json' \
          -d '{"username":"EU","password":"euAdminPw!"}' | jq -r .access_token)
```

The JWT is the access token (15 min default). The response also
carries a `refresh_token` (30 days) — store it; use `Refresh` to mint
a new access token before expiry. See [auth.md → JWT](auth.md#jwt-reseller--client).

---

## Step 3 — Owner sudo when needed

Need to act as a Reseller without their password? Login with the
Owner key in the Authorization header; any password is accepted, and
an `owner_sudo_login` event is emitted.

```bash
SUDO_JWT=$(curl -sk -H "Authorization: Bearer $OWNER" \
              -H 'Content-Type: application/json' \
              "$BASE/v1/auth:login" \
              -d '{"username":"EU","password":"anything"}' | jq -r .access_token)
```

The minted JWT carries `owner_sudo: true`. Use sparingly — every call
generates an audit row.

---

## Step 4 — Provision a Node

A *Node* is a logical group of Agents on one host (typically one VPN
server). Owner creates it via `AdminService`.

```bash
NODE_ID=$(curl -sk -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  "$BASE/v1/nodes" \
  -d '{
    "node": {
      "name":               "fra-1",
      "ips":                ["198.51.100.10"],
      "traffic_multiplier": 1.0,
      "geo":                {"country":"DE","city":"Frankfurt"},
      "service_hostnames":  ["vpn-fra.example.com"],
      "bandwidth_limit_bytes": 0
    }
  }' | jq -r '.id')
```

- `traffic_multiplier` scales counted bytes (premium nodes can charge
  e.g. 1.5×).
- `bandwidth_limit_bytes = 0` means unlimited. Sums of all Agent
  reports on this node can't exceed this when set.
- `service_hostnames` is used by `DomainCertificateService` to pick
  which certs go in the next `SyncConfig` payload for this node.

---

## Step 5 — Provision an Agent + its API key

```bash
# Create the agent row (xray on fra-1)
AGENT_ID=$(curl -sk -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  "$BASE/v1/agents" \
  -d "{
    \"agent\": {
      \"node_id\": \"$NODE_ID\",
      \"name\":    \"fra-1-xray\",
      \"kind\":    \"AGENT_KIND_XRAY\",
      \"version\": \"25.3.7\"
    }
  }" | jq -r '.id')

# Mint the agent's API key
AGENT_TOKEN=$(curl -sk -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  "$BASE/v1/apiKeys" \
  -d "{
    \"api_key\": {
      \"kind\":     \"API_KEY_KIND_AGENT\",
      \"owner_id\": \"$AGENT_ID\",
      \"name\":     \"fra-1-xray\",
      \"expires_at\": \"2027-01-01T00:00:00Z\"
    }
  }" | jq -r '.token')
```

The plaintext token comes back exactly once. Lose it → revoke and
reissue.

---

## Step 6 — Drop a config template into the Node

`Node.config` is a `map<string, structpb.Value>`. Per-version xray
templates live under dotted keys:

```
xray.numeric_version.25003007000   →  <inbound JSON for xray ≥ 25.03.07.00>
xray.numeric_version.26000000000   →  <inbound JSON for xray ≥ 26.00.00.00>
```

The encoding is `version.Encode("25.3.7") = 25_003_007_000`. The
Agent advertises its version on `SyncConfig`; HUE picks the largest
`<N> ≤ encode(agent_version)`. See
[agents.md](agents.md#template-example-xray-vless--xhttp) for a
template with placeholder markers (`{{.Users}}`, `{{getVar …}}`).

To update the map, `PATCH /v1/nodes/{id}` with the `config` field set.

---

## Step 7 — Reseller creates Clients

The EU reseller logs in (step 2), then provisions clients:

```bash
curl -sk -H "Authorization: Bearer $EU_JWT" -H 'Content-Type: application/json' \
  "$BASE/v1/clients" \
  -d '{
    "client": {
      "info": {
        "groups": ["premium"],
        "status": "CLIENT_STATUS_ACTIVE"
      },
      "auth_method": {
        "username": "alice",
        "password": "alicePw!"
      }
    }
  }'
```

`reseller_id` is auto-populated from the JWT for non-Owner callers
(non-Owner cannot move a Client across the tree). Owner can set
`reseller_id` explicitly.

`password` is stored AES-256-GCM-encrypted with `HUE_PASSWORD_ENC_KEY`;
decrypted on the fly during `Login` and during the agent-facing
`SyncConfig` rendering (downstream protocols like trojan / shadowsocks
/ RADIUS need plaintext-equivalent material).

---

## Step 8 — Agent reports usage

The Agent uses its API key (step 5) on `UsageService.ReportUsage`.

```bash
curl -sk -H "Authorization: Bearer $AGENT_TOKEN" -H 'Content-Type: application/json' \
  "$BASE/v1/usage:report" \
  -d "{
    \"report\": {
      \"client_id\":  \"$CLIENT_ID\",
      \"node_id\":    \"$NODE_ID\",
      \"agent_id\":   \"$AGENT_ID\",
      \"upload\":     1048576,
      \"download\":   4194304,
      \"session_id\": \"sess-1\",
      \"client_ip\":  \"203.0.113.5\"
    }
  }"
```

Response is the engine's decision:

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

If `shouldDisconnect: true`, drop the session at the Agent. `client_ip`
is read once for geo + session hashing then dropped — never persisted
or logged.

For high throughput, batch up to 1000 reports per call via
`POST /v1/usage:batchReport`.

---

## Step 9 — Day-to-day operations

### Inspect Clients

```bash
curl -sk -H "Authorization: Bearer $EU_JWT" \
  "$BASE/v1/clients?page_size=50"
```

Non-Owner sees only descendants of its own Reseller; Owner sees all.

### Suspend a Client

```bash
curl -sk -X PATCH -H "Authorization: Bearer $EU_JWT" -H 'Content-Type: application/json' \
  "$BASE/v1/clients/$CLIENT_ID" \
  -d '{"client":{"info":{"status":"CLIENT_STATUS_SUSPENDED"}}}'
```

The next `ReportUsage` for that Client returns `shouldDisconnect: true`.

### Reset usage at billing time

```bash
curl -sk -X POST -H "Authorization: Bearer $EU_JWT" \
  "$BASE/v1/clients/$CLIENT_ID:resetUsage"
```

Resets `usage_plans.current_*` and bumps `expires_at` according to
the plan's `reset_policy`. Aggregated counters on ancestor Resellers
stay (that's the audit trail).

### Recent events

```bash
curl -sk -H "Authorization: Bearer $OWNER" \
  "$BASE/v1/events?type=usage_recorded&page_size=100"
```

Useful filters: `login_locked_out`, `owner_sudo_login`, `cert_added`,
`client_suspended`, `node_quota_reached`, `penalty_applied`,
`usage_recorded`. See [auth.md](auth.md) and
[architecture.md](architecture.md) for the full list.

### Revoke an API key

```bash
curl -sk -X POST -H "Authorization: Bearer $OWNER" \
  "$BASE/v1/apiKeys/$KEY_ID:revoke"
```

Immediate — the interceptor reads `revoked_at` on every request.

### Move a Client across Resellers (Owner only)

```bash
curl -sk -X PATCH -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  "$BASE/v1/clients/$CLIENT_ID" \
  -d "{\"client\":{\"info\":{\"reseller_id\":\"$NEW_RESELLER_ID\"}}}"
```

Future usage propagates up the new chain; historical aggregates stay
on the old chain. For clean reassignment, reset the Client's plan
first.

---

## Certificates

Issue an ACME cert for one of the node's hostnames (requires
`HUE_ACME_CONTACT_EMAIL` set):

```bash
curl -sk -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  "$BASE/v1/domainCerts:requestACME" \
  -d '{"domain_names":["vpn-fra.example.com"]}'
```

Or BYO an existing cert via `POST /v1/domainCerts`. See
[certificates.md](certificates.md) for the full flow including
self-signed fallback.

---

## End-to-end recipe: "Stand up a 100-client reseller"

```bash
set -euo pipefail

# 1. Reseller + password
RES_PW=$(openssl rand -hex 16)
RES_ID=$(curl -sk -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
  "$BASE/v1/resellers" \
  -d "{\"reseller\":{\"name\":\"acme-corp\",\"password\":\"$RES_PW\"}}" \
  | jq -r .id)

# 2. Reseller logs in
RES_JWT=$(curl -sk "$BASE/v1/auth:login" -H 'Content-Type: application/json' \
            -d "{\"username\":\"acme-corp\",\"password\":\"$RES_PW\"}" \
            | jq -r .access_token)

# 3. 100 clients
for i in $(seq 1 100); do
  curl -sk -H "Authorization: Bearer $RES_JWT" -H 'Content-Type: application/json' \
    "$BASE/v1/clients" \
    -d "{
      \"client\": {
        \"info\":        {\"groups\":[\"acme\"]},
        \"auth_method\": {\"username\":\"acme-$i\",\"password\":\"$(openssl rand -hex 12)\"}
      }
    }" > /dev/null
done

# 4. Verify
curl -sk -H "Authorization: Bearer $RES_JWT" \
  "$BASE/v1/clients?page_size=1" | jq '.total_size // length'
# 100
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `401 missing Authorization header` | Header not set | Add `Authorization: Bearer <token>` |
| `401 malformed token` | Token doesn't start with `own_` / `agt_` and isn't a JWT | Re-check token shape; pasted prefix? |
| `401 unknown api key` | Token correct but no matching row | Either revoked or you're hitting a different deployment |
| `403 PermissionDenied … requires one of the allowed principal kinds` | RPC restricted to Owner / Agent / Reseller and you used the wrong principal | See [api.md](api.md) for the allow-list |
| `403` on Reseller listing Clients of another Reseller | Descendant scoping (correct behavior) | Use Owner key, or login as an ancestor |
| `409 AlreadyExists` on `CreateClient` | Username unique | Pick a different username |
| `429 too many failed login attempts` | Brute-force lockout (default 5 / 15 min) | Wait the window out; check `login_locked_out` events |
| Agent gets `Unimplemented` from `RequestACME` | `HUE_ACME_CONTACT_EMAIL` empty | Set the env, restart |
| `ReportUsage` always says `accepted: false` | Client has no active `UsagePlan` row | Create one via direct ent / SQL; CreateUsagePlan RPC is on the roadmap |
| Disconnect storms after a brief outage | Many Clients tripped `max_concurrent` as their apps reconnected from new IPs | Bump `HUE_CONCURRENT_WINDOW` (default 5 min) or `max_concurrent` per Client |

---

## Where the formal docs live

- **API surface** — [api.md](api.md) — every RPC, REST route, error code.
- **Auth deep dive** — [auth.md](auth.md) — token shapes, lockout, sudo, signing key rotation.
- **Configuration** — [configuration.md](configuration.md) — every `HUE_*` env var.
- **Architecture** — [architecture.md](architecture.md) — layers, single-port handler, engine flow.
- **Certificates** — [certificates.md](certificates.md) — BYO + ACME + self-signed.
- **Agents** — [agents.md](agents.md) — protocol-adapter contract + template rules.
- **Operations** — [operations.md](operations.md) — health probes, observability, runbook.
