# Configuration

HUE reads its configuration entirely from environment variables — no
config file, no command-line flags. The `Config` type is defined at
[hue.go](../hue.go); it lives at the root of the module so it's also
importable from external Go programs that embed HUE as a library.

Every variable is also addressable as `<NAME>_FILE` for secret-file
mounts (Docker / Kubernetes); when set, the file's contents replace the
direct value. Useful for `HUE_DB_URL_FILE`, `HUE_BOOTSTRAP_TOKEN_FILE`.

## Required

| Variable | Default | Notes |
|---|---|---|
| `HUE_DB_URL` | — | Postgres DSN, e.g. `postgres://hue:pass@host:5432/hue?sslmode=require` |

The binary refuses to start without it.

## Networking

| Variable | Default | Notes |
|---|---|---|
| `HUE_ADDR` | `:8443` | TCP listen address |
| `HUE_TLS_CERT` | "" | path to TLS certificate (PEM) |
| `HUE_TLS_KEY` | "" | path to TLS private key (PEM) |

If both `HUE_TLS_CERT` and `HUE_TLS_KEY` are set, the listener serves
TLS; otherwise it uses h2c (cleartext HTTP/2). h2c is intended for dev
and behind a TLS-terminating proxy only — never expose it directly.

## Database

| Variable | Default |
|---|---|
| `HUE_AUTO_MIGRATE` | `false` |
| `HUE_DB_MAX_OPEN_CONNS` | `20` |
| `HUE_DB_MAX_IDLE_CONNS` | `10` |

Production should set `HUE_AUTO_MIGRATE=false` and apply migrations as a
separate Job before the Deployment rolls out. Auto-migrate is fine for
dev and the very first deploy.

## Engine

| Variable | Default | Notes |
|---|---|---|
| `HUE_CONCURRENT_WINDOW` | `5m` | sliding window for distinct-IP counting |
| `HUE_PENALTY_DURATION` | `10m` | how long a user stays in penalty after exceeding `max_concurrent` |
| `HUE_MAXMIND_DB_PATH` | "" | path to `GeoLite2-City.mmdb`; if empty, geo lookups return zero values |

## Auth

| Variable | Default | Notes |
|---|---|---|
| `HUE_BOOTSTRAP_TOKEN` | "" | first-run only — provisions a root manager + manager API key with this plaintext token. Idempotent: skipped if any non-revoked manager key already exists. **Remove from your env / secret after first start.** |

The token must be in the canonical `mgr_<base32 body>` shape produced by
`auth.GenerateKey(KindManager)`. See [auth.md](auth.md).

## Logging

| Variable | Default |
|---|---|
| `HUE_LOG_LEVEL` | `info` (`debug`, `info`, `warn`, `error`) |
| `HUE_LOG_FORMAT` | `json` (`json` or `text`) |

Logs go to **stderr** as `log/slog` records.

## Shutdown

| Variable | Default |
|---|---|
| `HUE_SHUTDOWN_TIMEOUT` | `30s` |

On `SIGINT` / `SIGTERM`, HUE flips its standard gRPC health to
`NOT_SERVING`, then drains in-flight requests with this timeout, then
calls `grpcServer.GracefulStop()` and closes the ent client.

## Reading config in tests or from external programs

The `Config` type is at the module root, so you can construct it
directly or call `LoadConfig` to read from the environment:

```go
import "github.com/hiddify/hue"

ctx := context.Background()
t.Setenv("HUE_DB_URL", "postgres://...")
cfg, err := hue.LoadConfig(ctx)
```

Or build it programmatically and skip env entirely:

```go
cfg := &hue.Config{
    Addr:             ":8443",
    DatabaseURL:      "postgres://hue:hue@localhost:5432/hue?sslmode=disable",
    AutoMigrate:      true,
    ConcurrentWindow: 5 * time.Minute,
    PenaltyDuration:  10 * time.Minute,
    ShutdownTimeout:  30 * time.Second,
}
if err := hue.Run(ctx, cfg, logger); err != nil {
    log.Fatal(err)
}
```
