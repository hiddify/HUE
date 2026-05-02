# pkg/clients — protocol adapters

This package tree holds HUE's protocol adapters: thin shims that let the
engine talk to a running service (xray, singbox, wireguard, OpenVPN, …)
through one Go interface, no matter what that service speaks on the wire.

Layout:

```
pkg/clients/
├── clients.go          ← the common Client interface + Capability flags
├── template/           ← copy-paste starting point for a new protocol
└── xray/               ← representative implementation against xray-core's gRPC API
```

## The contract

Every adapter implements [`clients.Client`](clients.go):

```go
type Client interface {
    Name() string
    Capabilities() Capability
    Healthcheck(ctx context.Context) error
    ReadStats(ctx context.Context) ([]UsageDelta, error)
    Disconnect(ctx context.Context, u User) error
    AddUser(ctx context.Context, u User, secret string) error
    RemoveUser(ctx context.Context, u User) error
    Close() error
}
```

Anything an adapter can't do returns `clients.ErrUnsupported` and the
matching `Capability` bit is omitted from `Capabilities()`. The engine
checks the bitmask before calling, so unsupported features become
fast-path no-ops instead of errors during a request.

## Adding a new adapter

```bash
cp -r pkg/clients/template pkg/clients/foo
# Replace template/Template with foo/Foo throughout
```

Then for each method:

1. Decide whether the protocol supports it. If yes, OR the matching
   `Capability` bit into `Capabilities()` and implement.
2. If no, leave the body returning `clients.ErrUnsupported`.
3. Keep all protocol-specific types and helpers in
   `pkg/clients/foo/` — only `clients.User`, `clients.UsageDelta`, and
   error sentinels should leak through the interface.

The `_test.go` companion should fake the protocol's wire format
(`net.Pipe` for gRPC; an in-memory CLI for `wg`-style adapters) so
adapter tests don't need a live service.

## Why a separate package tree?

- **`pkg/clients/...` is a public import path.** External Go programs
  that want to drive a HUE-managed xray instance can `go get
  github.com/hiddify/hue/pkg/clients/xray` and use the adapter
  directly, without depending on HUE's server internals.
- **One adapter per directory means one set of dependencies per
  adapter.** The xray adapter pulls only what xray needs; the
  WireGuard one stays free of gRPC machinery; etc.
- **Templates beat inheritance.** New adapters start from a copy and
  diverge as needed — not as subclasses of an abstract base whose
  shape pulls them toward incidental complexity.

## State of the world today

| Adapter | Status |
|---|---|
| `template/` | Copy-paste skeleton — do not import as a real client. |
| `xray/` | Healthcheck implemented; stats / disconnect / provisioning return `ErrUnsupported` and need xray-core's wire-format messages plumbed in. See the package doc comment for the exact RPCs to call. |

Implementations for singbox, wireguard, OpenVPN, RADIUS, etc. follow
the same template pattern when needed.
