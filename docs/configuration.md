# Configuration

HUE reads its configuration entirely from environment variables — no
config file, no command-line flags. The `Config` type is defined at
[hue.go](../hue.go); it lives at the root of the module so it's also
importable from external Go programs that embed HUE as a library.

Every variable is also addressable as `<NAME>_FILE` for secret-file
mounts (Docker / Kubernetes); when set, the file's contents replace
the direct value. Useful for `HUE_DB_URL_FILE`,
`HUE_BOOTSTRAP_TOKEN_FILE`, `HUE_PASSWORD_ENC_KEY_FILE`.

## Required

| Variable | Default | Notes |
|---|---|---|
| `HUE_DB_URL` | — | Postgres DSN, e.g. `postgres://hue:pass@host:5432/hue?sslmode=require`. The binary refuses to start without it. |

## Strongly recommended in production

| Variable | Default | Notes |
|---|---|---|
| `HUE_PASSWORD_ENC_KEY` | "" (pass-through) | 64-hex-char (32-byte) AES-256-GCM master key. Encrypts Client passwords, Client private keys, DomainCertificate private keys, and JWT signing keys at rest. With an empty value, those columns store plaintext (dev/test fallback). Tests set the env to force the encryption path. |

## Networking

| Variable | Default | Notes |
|---|---|---|
| `HUE_ADDR` | `:8443` | TCP listen address |
| `HUE_TLS_CERT` | "" | path to TLS certificate (PEM) |
| `HUE_TLS_KEY` | "" | path to TLS private key (PEM) |

If both `HUE_TLS_CERT` and `HUE_TLS_KEY` are set, the listener serves
TLS; otherwise it uses h2c (cleartext HTTP/2). h2c is intended for
dev and behind a TLS-terminating proxy only — never expose it
directly.

## Database

| Variable | Default |
|---|---|
| `HUE_AUTO_MIGRATE` | `false` |
| `HUE_DB_MAX_OPEN_CONNS` | `20` |
| `HUE_DB_MAX_IDLE_CONNS` | `10` |

Production should set `HUE_AUTO_MIGRATE=false` and apply migrations
as a separate Job before the Deployment rolls out. Auto-migrate is
fine for dev and the very first deploy.

## Engine

| Variable | Default | Notes |
|---|---|---|
| `HUE_CONCURRENT_WINDOW` | `5m` | sliding window for distinct-IP counting |
| `HUE_PENALTY_DURATION` | `10m` | how long a Client stays in penalty after exceeding `max_concurrent` |
| `HUE_MAXMIND_DB_PATH` | "" | path to `GeoLite2-City.mmdb`; if empty, geo lookups return zero values |

## Bootstrap

| Variable | Default | Notes |
|---|---|---|
| `HUE_BOOTSTRAP_TOKEN` | "" | First-run only — provisions an Owner API key with this plaintext token. Idempotent: skipped if any non-revoked Owner key already exists. **Remove from your env / secret after first start.** Must start with `own_`. |

See [auth.md → Bootstrap](auth.md#bootstrap).

## ACME

| Variable | Default | Notes |
|---|---|---|
| `HUE_ACME_DIRECTORY_URL` | "" (Let's Encrypt prod) | Override with the staging URL during testing |
| `HUE_ACME_CONTACT_EMAIL` | "" | Required to enable `DomainCertificateService.RequestACME`. The RPC returns `Unimplemented` while empty |

The HTTP-01 challenger is mounted at
`/.well-known/acme-challenge/` on HUE's own listener regardless of
these env vars; the variables only gate the RPC. See
[certificates.md](certificates.md).

## JWT signing

JWT signing keys are auto-generated (ed25519) on first boot and
persisted to the `signing_keys` table. The private key is AES-GCM
encrypted with `HUE_PASSWORD_ENC_KEY`. Rotation works without
invalidating in-flight tokens (verifier walks every non-revoked row).
No env var to configure.

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
`NOT_SERVING`, then drains in-flight requests with this timeout,
then calls `grpcServer.GracefulStop()` and closes the ent client.

## Reading config from external programs

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
    ACMEContactEmail: "ops@example.com",
}
if err := hue.Run(ctx, cfg, logger); err != nil {
    log.Fatal(err)
}
```
