# Operations

## Health checks

HUE implements both the standard gRPC health service
([`grpc.health.v1.Health`](https://github.com/grpc/grpc/blob/master/doc/health-checking.md))
and a REST-mapped equivalent at `/healthz`.

```bash
# REST
curl -sk https://hue:8443/healthz
# {"status":"HEALTH_STATUS_SERVING"}

# Native gRPC
grpc_health_probe -addr hue:8443 -tls -tls-no-verify
# status: SERVING
```

The probes report `NOT_SERVING` while:

- The DB connection isn't ready (rejected by `database.Open`'s ping).
- A graceful shutdown has started — `bundle.Shutdown()` flips health
  before the gRPC server stops accepting new requests, so load balancers
  drain naturally.

Kubernetes Liveness, Readiness, and Startup probes all `exec` the
binary's `healthcheck` subcommand to avoid a curl dependency. See
[deployments/k8s/deployment.yaml](../deployments/k8s/deployment.yaml).

## Logging

Structured JSON to stderr by default. Format and level via
`HUE_LOG_FORMAT` and `HUE_LOG_LEVEL` (see [configuration.md](configuration.md)).

Every gRPC method emits one log line at the end of the call:

```json
{
  "time": "2026-05-01T03:42:11.123Z",
  "level": "INFO",
  "msg": "rpc",
  "method": "/hue.v1.ResellerClientService/CreateClient",
  "dur": "12.4ms",
  "code": "OK"
}
```

For non-OK responses the level is `WARN`. Bodies are not logged — use a
debug-only logger if you need that.

## Graceful shutdown

On `SIGINT` / `SIGTERM`:

1. `bundle.Shutdown()` flips `grpc.health.v1.Health` to `NOT_SERVING`,
   so external load balancers stop routing new traffic.
2. `httpSrv.Shutdown(ctx)` stops accepting new HTTP/REST connections and
   waits for in-flight requests, up to `HUE_SHUTDOWN_TIMEOUT` (default 30s).
3. `grpcSrv.GracefulStop()` does the same for native gRPC.
4. Deferred `dbClient.Close()` releases the Postgres pool.
5. Deferred `geoResolver.Close()` releases the MaxMind handle.

If the shutdown timeout is exceeded, the process exits non-zero and the
remaining requests are dropped — pick a value that comfortably exceeds
your slowest legitimate request.

## Observability hooks

- **Logs**: stdout/stderr; ship via your platform's collector.
- **Health probes**: `/healthz` (REST) and `grpc.health.v1.Health` (gRPC).
- **Tracing**: not yet wired. The interceptor chain is ready for
  OpenTelemetry but the exporter is a follow-up. Track it under "Phase
  10 — observability" in your tracker.
- **Metrics**: not yet wired. `/metrics` (Prometheus) is on the roadmap.

The `prometheus.io/*` annotations on the Deployment template are
already present so once metrics ship, scrape config is one helm
release away.

## Common runbook entries

### Spike in `quota_used` plan statuses

A Client is reaching their plan limit. Check
`SELECT id, client_id, current_total, total_limit, status FROM usage_plans WHERE
status = 'quota_used' ORDER BY updated_at DESC LIMIT 20;`. If the Client
should keep going, either bump their plan limits or call
`POST /v1/clients/{client_id}:resetUsage`.

### Penalty storm

Symptoms: many Clients in `penalty` status simultaneously. Most likely
cause is misconfigured `max_concurrent` — a Client app reconnects from
multiple IPs (mobile-network NAT, VPN failover) and hits the limit.

Check the configured values:

```sql
SELECT client_id, max_concurrent, current_total
FROM usage_plans WHERE status = 'penalty';
```

The penalty store is in-memory; restart drops it.

### Lockout flood

Symptoms: a surge of `login_locked_out` events for one `(ip, username)`
pair. Check via:

```bash
curl -sk -H "Authorization: Bearer $OWNER" \
  "$BASE/v1/events?type=login_locked_out&page_size=50"
```

Likely a brute-force attempt or a misconfigured client. Lockout state
is in-memory (LRU); restart clears it. To raise the threshold, change
the `auth.NewLockout(maxAttempts, window)` call site in `hue.go`.

### A subscriber is dropping events

The eventstore logs every drop:

```json
{"level":"WARN","msg":"event dropped: subscriber buffer full",
 "subscriber_id":"...", "event_type":"usage_recorded", "total_drops":42}
```

If you see `total_drops` climbing fast, either the subscriber is too
slow (consume from a separate goroutine + persistent backing store) or
the buffer is too small (pass a larger value to `Subscribe`). Don't
silently raise without understanding why — the legacy bug was a silent
drop.

### Postgres connection exhaustion

Watch for `pq: too many connections`. `HUE_DB_MAX_OPEN_CONNS` defaults
to 20; with N replicas, total open connections is `N * 20`. Either
increase Postgres's `max_connections`, drop the per-instance limit, or
front Postgres with PgBouncer.

## Known-unimplemented RPCs

These return `Unimplemented` or stubbed responses today; track as
roadmap items:

- `AdminService.StreamEvents` — gateway route exists; the server
  returns an empty stream.
- `ConfigService.Heartbeat` — `disconnect_client_ids` hints are
  always empty.
- `DomainCertificateService.RequestACME` — returns `Unimplemented`
  until `HUE_ACME_CONTACT_EMAIL` is set; functional once set.

Adding any of these is the same pattern: write the proto-to-server
shim in `internal/server/<file>.go` and put the business logic in
`internal/service/`. No proto edits, no codegen, no manual HTTP route
wiring.
