# Agents — protocol adapters

HUE's `pkg/agents/<protocol>/` packages are the protocol adapters.
One per protocol; each lives in its own subpackage so its
dependencies don't bleed into the rest of the binary.

## Contract

```go
type Agent interface {
    Name() string             // "xray", "wireguard", ...
    Capabilities() Capability
    Healthcheck(ctx) error

    // pull HUE config + render + apply
    SyncConfig(ctx) (changed bool, err error)

    // post-sync, on a running instance, via the protocol's admin API
    AddUser(ctx, u User, secret string) error
    RemoveUser(ctx, u User) error
    Disconnect(ctx, u User) error
    Close() error
}
```

Each protocol's struct type stays `Client` inside its own subpackage
(`xray.Client`, `wireguard.Client`, `template.Client`) — the
shared interface is `agents.Agent`.

Unsupported methods return `agents.ErrUnsupported` and the matching
`Capability` bit stays unset. Engine code probes `Capabilities()` first
so unsupported features are fast-path no-ops instead of errors during
a request.

## SyncConfig pipeline

Each adapter takes a `agents.SyncConfigFunc` callback that wraps
`huev1.ConfigServiceClient.SyncConfig`. The contract is:

```go
type SyncConfigFunc func(ctx, currentEtag string) (ConfigSnapshot, error)

type ConfigSnapshot struct {
    Template       string                  // verbatim from HUE
    TemplateFormat string                  // "xray-json", "wireguard-ini", ...
    Vars           map[string]string       // small per-node overrides
    Users          []ConfigUser            // active Clients
    Etag           string
    Changed        bool
}
```

The adapter's `Renderer` consumes the snapshot and emits
protocol-specific bytes. The Renderer is a small interface:

```go
type Renderer interface {
    Name() string
    AcceptsFormat(format string) bool
    Render(snap ConfigSnapshot) ([]byte, error)
}
```

Renderers use Go's `text/template` to support iteration over `.Users`
+ named helpers (`getVar`, `quote`, `default`, `json`, `atoi`). The
template lives in HUE's `Node.config` under the right
`<kind>.numeric_version.<N>` key (see
[phase2-changes.md](phase2-changes.md) for the version-selection rule).

Output is fed to `ApplyConfigFunc` — typically:
- write to disk + send SIGHUP / `wg syncconf`,
- POST to xray's `HandlerService.AlterInbound`,
- or hand off to a sidecar.

## Shipped today

| Adapter | Source | Status |
|---|---|---|
| `pkg/agents/xray/` | xray.go, config.go | Renderer + SyncConfig working; Stats/Disconnect/Provision stubbed (need xray-core proto plumbing) |
| `pkg/agents/wireguard/` | wireguard.go | Renderer + SyncConfig working; Stats/Disconnect/Provision stubbed (need `wg` exec wiring) |
| `pkg/agents/template/` | template.go | Copy-paste skeleton; returns `ErrUnsupported` everywhere |

## Adding a new protocol

```bash
cp -r pkg/agents/template pkg/agents/foo
# Replace template/Template with foo/Foo throughout.
# Decide which Capability bits the protocol supports; OR them in.
# Implement those methods. Leave the rest returning ErrUnsupported.
```

Per-protocol types stay inside `pkg/agents/foo/`; only
`agents.ConfigUser`, `agents.ConfigSnapshot`, and the error
sentinels leak through the interface. Each adapter ships an `_test.go`
that fakes the wire format (`net.Pipe` for gRPC, an in-memory CLI for
`wg`-style adapters).

## Template example (xray, vless + xhttp)

```
{
  "inbounds": [{
    "port":     {{.Vars.port}},
    "protocol": "vless",
    "settings": {
      "decryption": "none",
      "clients": [
        {{- range $i, $u := .Users -}}
        {{- if $i}},{{end}}
        { "id": {{$u.ID | quote}}, "email": {{$u.Username | quote}} }
        {{- end -}}
      ]
    },
    "streamSettings": {
      "network": "xhttp",
      "xhttpSettings": {
        "path": {{getVar "xray.xhttp.path" "/api" | quote}}
      }
    }
  }]
}
```

Operator stores this under `Node.config["xray.numeric_version.25003007000"]`
(or whatever encoded version represents the minimum xray-core release
this template is valid for). An xray instance running 25.4.0 will pick
this exact template; a 25.7.0 instance will pick a later
`numeric_version` if one exists.

## Why placeholders / templates instead of structured config?

The structured-config approach (one Go type per protocol with typed
fields) doesn't scale: xray-core ships dozens of transport profiles +
security layers + tweaks per release. HUE never tries to model that.
The template is the API.

When a brand-new transport ships in xray-core 26.x, the operator
authors a template referencing whatever new JSON shape xray needs,
stores it under `xray.numeric_version.26000000000`, and clients
running 26.x pick it up automatically. **No code change in HUE.**
