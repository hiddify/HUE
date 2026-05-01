# Development

## Prerequisites

- Go 1.26 (auto-fetched by the toolchain directive in `go.mod` if you have 1.21+).
- Docker for the integration / e2e test stacks.
- [`buf`](https://buf.build) for proto codegen — install with `make tools`.

The full developer toolchain is one command:

```bash
make tools
```

This installs `buf`, `golangci-lint`, `govulncheck`, `ent`, and `air`
into `$GOPATH/bin`.

## Common loops

```bash
make help                # show every Make target
make build               # bin/hue
make run                 # go run ./cmd/hue with debug logs
make test                # unit tests with -race -short
make test-integration    # spin up Postgres via testcontainers
make test-e2e            # docker compose stack + e2e suite
make ci                  # exact CI gate locally (lint + tests + e2e)
```

Hot-reload during development:

```bash
docker compose -f deployments/docker/docker-compose.yml --profile dev up
```

This brings up Postgres + a hue container running `air` with the watch
config in [deployments/docker/.air.toml](../deployments/docker/.air.toml).
Save a `.go` or `.proto` file and the container rebuilds + restarts.

## Regenerating proto

When you edit `api/proto/hue/v1/hue.proto`:

```bash
make proto
```

This runs `buf lint`, `buf format --diff --exit-code`, then `buf generate`
to refresh `gen/go/...` and `gen/openapi/...`. The CI gate
([.github/workflows/ci.yml](../.github/workflows/ci.yml)) re-runs `buf
generate` and fails if `git diff` is non-empty, so you must commit the
generated files alongside the proto change.

The generated files live in `gen/` and are gitignored locally; re-run
`make proto` after a fresh checkout. (We may switch to committing them
later — the trade-off is build hermeticity vs. repo size.)

`buf breaking` runs against the PR's base branch in CI to detect
backward-incompatible proto changes.

## Regenerating ent

When you edit a schema in `internal/ent/schema/`:

```bash
make ent
```

This runs `go generate ./internal/ent/...`, which produces 80+ typed-client
files under `internal/ent/`. Schemas are tracked in git; the generated
code is gitignored. Always run `make ent` after editing a schema and
before running tests.

## Linting

```bash
make lint        # gofmt + go vet + golangci-lint
make govulncheck # CVE scan
```

`golangci-lint` config and the strict ruleset are picked up from
[golangci.yml] (TODO: add). The CI run uses the latest tagged release.

## Running tests in different modes

```bash
go test -race -short ./...                    # fast, default
go test -race ./...                           # all tests, including ones marked long
go test -race -tags=integration ./...         # add integration tests (need Postgres)
go test -tags=e2e ./test/e2e/...              # only against a running stack
go test -fuzz=Fuzz<Name> -fuzztime=1m ./...   # fuzz one target
```

The integration tests use [`testcontainers-go`](https://golang.testcontainers.org)
to start a real Postgres on demand; no global setup is required, the
container starts and stops per test binary.

## Cutting a release

1. Tag the commit on `main` with `vX.Y.Z`.
2. Push the tag — `.github/workflows/release.yml` runs:
   - re-runs the full CI gate
   - builds multi-arch Docker images, signs with cosign, pushes to GHCR
   - generates SPDX SBOM and SLSA provenance
   - publishes Go binaries via GoReleaser
   - attaches the OpenAPI spec to the GitHub release

## Common gotchas

- **"toolchain not available"** — Go 1.26 patch versions land on darwin/arm64
  later than linux/amd64. Set `GOTOOLCHAIN=go1.26.1` in your shell to pin
  to a version that's published. `make` already does this implicitly via
  `go.mod`'s `toolchain` directive.
- **`buf generate` succeeds but tests fail to find generated types** —
  run `go mod tidy`. Adding new proto imports usually pulls in new module
  deps that aren't recorded yet.
- **ent compile error after schema edit** — run `make ent`. The generated
  files in `internal/ent/` are stale.
- **gRPC-gateway PR but no REST route** — your RPC is missing
  `option (google.api.http) = { ... }`. The CI proto job catches this.
