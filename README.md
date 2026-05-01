# HUE — Hiddify Usage Engine

A protocol-agnostic Go control plane that VPN/proxy nodes (Xray, Singbox,
WireGuard, OpenVPN, RADIUS, …) report usage to. HUE enforces quota,
counts concurrent sessions, applies penalties, rolls usage up through a
multi-level reseller hierarchy, and exposes the whole API surface as
**gRPC + REST on a single port**.

The wire contract — types, RPCs, validation rules, and HTTP routes — is
defined exactly once in [`api/proto/hue/v1/hue.proto`](api/proto/hue/v1/hue.proto).
`buf generate` produces the gRPC server stubs, the gRPC-gateway HTTP
router, and the OpenAPI spec from that single file. There is no
hand-written REST dispatch.

## What you get

- **One TLS port** serves browsers, gRPC clients, and gRPC-gateway-translated REST traffic. Auth + validation interceptors apply uniformly to both transports.
- **PostgreSQL via [ent](https://entgo.io)** — schema-as-code, generated typed client, no reflection in the hot path. Engine-agnostic enough to run unit tests on SQLite in-memory.
- **Per-actor API keys**, Argon2id-hashed at rest, lookup-prefix routed, constant-time verified. Three actor kinds: `manager`, `service`, `node`.
- **Privacy by construction** — client IPs flow through geo + session counting and are dropped before any persistence or log write.
- **Fine-grained per-user locking** with delete-on-forget cleanup so the legacy unbounded-growth bug can't come back.
- **Multi-level manager hierarchy** with projected-usage checks before commit, so a parent's limit is never violated even with concurrent updates on siblings.

## Quickstart

```bash
# Bring up Postgres + HUE with TLS
mkdir -p deployments/docker/{secrets,certs,geo}
echo "supersecret"   > deployments/docker/secrets/pg_password
printf 'postgres://hue:supersecret@postgres:5432/hue?sslmode=disable' \
  > deployments/docker/secrets/db_url
echo "mgr_$(openssl rand -base64 18 | tr -d '/+=' | tr 'A-Z' 'a-z')" \
  > deployments/docker/secrets/bootstrap_token
# (drop a self-signed cert into deployments/docker/certs/tls.{crt,key})

docker compose -f deployments/docker/docker-compose.yml up -d
TOKEN=$(cat deployments/docker/secrets/bootstrap_token)

# REST (via grpc-gateway)
curl -sk -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/v1/users \
  -H 'Content-Type: application/json' \
  -d '{"user":{"info":{"groups":["test"]},"auth_method":{"username":"u1","password":"p1"}}}'

# Native gRPC — same port, same handler
grpcurl -insecure \
  -H "authorization: Bearer $TOKEN" \
  -d '{"page_size":10}' \
  localhost:8443 hue.v1.AdminService/ListUsers
```

Both calls hit the same Go function. See [docs/api.md](docs/api.md) for the
full surface.

## Architecture in one diagram

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│ gRPC client  │     │ HTTP/REST    │     │  browser     │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │ HTTP/2             │ HTTP/1.1 or 2      │ HTTP/1.1
       │ application/grpc   │ application/json   │
       └──────────┬─────────┴───────────┬────────┘
                  │                     │
              ┌───┴─────────────────────┴───┐
              │  cmd/hue/main.go            │   one TLS port (h2c when no TLS)
              │  ┌──────────────────────┐   │
              │  │ content-type sniff   │   │
              │  └──────┬──────┬────────┘   │
              │         │      │            │
              │   gRPC  │      │ gateway    │
              │  server │      │ ServeMux   │
              │         │      │            │
              │         │   bufconn         │
              │         │   (in-process)    │
              │         ▼                   │
              │  ┌──────────────────────┐   │
              │  │ unary interceptors:  │   │
              │  │  panic recovery      │   │
              │  │  slog                 │   │
              │  │  auth (Argon2id)      │   │
              │  │  protovalidate (CEL) │   │
              │  └──────┬───────────────┘   │
              │         │                   │
              │  ┌──────┴───────────────┐   │
              │  │ internal/server/     │   │
              │  │ thin proto↔ent shims │   │
              │  └──────┬───────────────┘   │
              │         │                   │
              │  ┌──────┴───────────────┐   │
              │  │ internal/service/    │   │
              │  │  Engine, locks,      │   │
              │  │  sessions, penalty,  │   │
              │  │  manager hierarchy   │   │
              │  └──────┬───────────────┘   │
              │         │                   │
              │  ┌──────┴────┐  ┌──────────┐│
              │  │ ent client │  │ events  ││
              │  └──────┬────┘  └──────────┘│
              └─────────┼──────────────────┘
                        │
                  ┌─────┴──────┐
                  │ PostgreSQL │
                  └────────────┘
```

## Project layout

```
HUE/
├── api/proto/hue/v1/hue.proto   # SINGLE SOURCE OF TRUTH for the API
├── buf.yaml, buf.gen.yaml       # Codegen pipeline
├── gen/                         # Generated (gitignored): pb, grpc, gw, openapi
├── cmd/hue/main.go              # Entry point — single-port handler
├── internal/
│   ├── ent/                     # Generated client; schemas in ./schema
│   ├── server/                  # Proto↔ent shims, interceptors
│   ├── service/                 # Business logic (engine, locks, hierarchy)
│   ├── auth/                    # API keys + interceptor + bootstrap
│   ├── database/                # Postgres + ent wiring
│   ├── eventstore/              # Audit events + pub/sub fan-out
│   ├── geo/                     # MaxMind lookup with zero-IP retention
│   └── config/                  # envconfig-driven config
├── deployments/
│   ├── docker/                  # Multi-stage Dockerfile + compose stacks
│   └── k8s/                     # Manifests: deployment, service, hpa, …
├── .github/workflows/           # CI, release, proto-diff, nightly, codeql
├── docs/                        # Documentation index (start at docs/README.md)
├── PRD.md                       # Product requirements
├── REVIEW.md                    # Audit of the previous (v1) codebase
└── Makefile                     # `make help` lists everything
```

## Documentation

- [How-to manual](docs/manual.md) — task-oriented walkthrough from bootstrap to daily ops
- [Architecture](docs/architecture.md) — components, data flow, the single-port handler
- [API reference](docs/api.md) — services, RPCs, REST routes, examples
- [Configuration](docs/configuration.md) — every `HUE_*` env var
- [Authentication](docs/auth.md) — API keys, bootstrap, revocation
- [Development](docs/development.md) — local setup, regenerating proto + ent
- [Deployment](docs/deployment.md) — Docker compose and Kubernetes
- [Operations](docs/operations.md) — health, observability, shutdown, troubleshooting

The product spec lives in [PRD.md](PRD.md). The audit of the previous
implementation, including the regression bugs that the rewrite fixes,
lives in [REVIEW.md](REVIEW.md).

## License

[MIT](LICENSE).
