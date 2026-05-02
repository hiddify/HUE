// Package clients defines the common surface every protocol adapter
// implements so HUE can talk to a running service (xray, singbox,
// wireguard, OpenVPN, …) the same way regardless of what that service
// is on the wire.
//
// The interface is intentionally small. A concrete implementation:
//
//   - reports its Capabilities so the engine can probe what's supported
//     before calling (no surprise UNIMPLEMENTED in the hot path);
//   - returns clients.ErrUnsupported from any method whose capability
//     bit isn't set;
//   - keeps protocol-specific config in its own pkg/clients/<name>
//     package and leaks no proto-specific types through this interface.
//
// To add a new protocol, copy pkg/clients/template/ to
// pkg/clients/<name>/ and fill in the bodies. See pkg/clients/README.md.
package clients

import (
	"context"
	"errors"
	"time"
)

// User is HUE's view of a service-side principal. The Tag is the
// protocol-native identifier (xray uses email-shaped strings, WireGuard
// uses base64 public keys, OpenVPN uses CN, …) — adapters translate
// between Tag and HUE's UUID.
type User struct {
	ID      string // HUE's user UUID
	Tag     string // protocol-native identifier
	Inbound string // protocol-native inbound / interface name (optional)
}

// UsageDelta is the bytes-since-last-poll for one user. Adapters that
// can't atomically reset counters must document the resulting at-most-
// once / at-least-once semantics in their package doc.
type UsageDelta struct {
	User     User
	Upload   int64
	Download int64
	At       time.Time
}

// Capability is a bitmask of what an adapter implements. Engine code
// inspects Capabilities() before calling so an unsupported feature is a
// fast-path no-op, not an error in the request flow.
type Capability uint32

const (
	CapHealthcheck Capability = 1 << iota
	CapStats
	CapDisconnect
	CapProvision  // AddUser + RemoveUser
	CapConfigSync // SyncConfig — fetch + apply config from HUE
)

// Has reports whether c includes every bit in want.
func (c Capability) Has(want Capability) bool { return c&want == want }

// Client is the protocol-agnostic surface.
//
// All methods take a context and respect its deadline / cancellation.
// Implementations must be goroutine-safe — the engine calls Healthcheck
// concurrently with ReadStats and Disconnect.
type Client interface {
	// Name returns a stable, lowercase identifier for the protocol —
	// "xray", "singbox", "wireguard", … Used in metrics, logs, and the
	// hue.v1.Protocol enum.
	Name() string

	// Capabilities reports what this client implements.
	Capabilities() Capability

	// Healthcheck is a fast probe: typically a no-op RPC against the
	// service, or a CLI exit-code check. Returns nil iff the service
	// is reachable and ready to accept commands.
	Healthcheck(ctx context.Context) error

	// ReadStats returns one UsageDelta per user the service has counted
	// since the previous successful call, then atomically resets the
	// counters. Implementations that can't reset atomically must
	// document their semantics.
	ReadStats(ctx context.Context) ([]UsageDelta, error)

	// Disconnect terminates active sessions for u. The service may need
	// some time to actually drop the TCP/UDP flow; this method returns
	// after the command is acknowledged, not after the session is
	// gone on the wire.
	Disconnect(ctx context.Context, u User) error

	// AddUser provisions u with the given protocol-native secret
	// (UUID, public key, password, …). Idempotent — calling on an
	// existing user is a no-op.
	AddUser(ctx context.Context, u User, secret string) error

	// RemoveUser deprovisions u. Idempotent.
	RemoveUser(ctx context.Context, u User) error

	// SyncConfig pulls the adapter's current key-value config from HUE
	// (via NodeService.SyncConfig), runs it through the adapter's
	// ConfigGenerator, and applies the result locally. Reports whether
	// the local config was actually replaced (false when the HUE
	// response says "unchanged" — etag matched). Adapters that don't
	// participate in config sync return ErrUnsupported.
	SyncConfig(ctx context.Context) (changed bool, err error)

	// Close releases adapter resources (gRPC conns, file handles, …).
	Close() error
}

// ConfigUser is HUE's view of one user the service should serve.
// ID doubles as the vless UUID; PublicKey is for WireGuard etc. The
// renderer picks whatever fields its protocol needs.
type ConfigUser struct {
	ID        string
	Username  string
	PublicKey string
	Groups    []string
}

// ConfigSnapshot is what HUE returns from NodeService.SyncConfig. The
// adapter's renderer substitutes the template using Vars and Users to
// produce the protocol-specific config bytes the service consumes.
//
// Templates use Go's text/template syntax — `{{.Vars.port}}`,
// `{{range .Users}}{{.ID}}{{end}}` etc. — so the same template can
// emit a vless inbound, a wireguard config, multiple parallel
// transports on the same service, REALITY blocks, anything the
// underlying protocol supports without code changes in HUE.
type ConfigSnapshot struct {
	// Template is the literal protocol-native config with template
	// placeholders. Empty when Changed is false.
	Template string
	// TemplateFormat hints which renderer to use ("xray-json",
	// "wireguard-ini", …). Renderers reject unknown formats.
	TemplateFormat string
	// Vars are small per-service overrides referenced via
	// {{.Vars.<key>}}. Keys MAY be dotted ("xray.xhttp.path").
	Vars map[string]string
	// Users is the active set of users the service should serve, as
	// reported by HUE at request time.
	Users []ConfigUser
	// Etag is the snapshot identifier; pass back as currentEtag on
	// the next call to short-circuit when nothing changed.
	Etag string
	// Changed is true when Template/Vars/Users carry fresh data, false
	// when the response is a 304-equivalent (caller already up to date).
	Changed bool
}

// SyncConfigFunc is the callback an adapter uses to fetch the current
// config snapshot from HUE. Application code wires this to a real
// huev1.NodeServiceClient.SyncConfig wrapper; tests inject a fake.
// Keeping the adapter free of huev1 imports keeps the public package
// tree light.
type SyncConfigFunc func(ctx context.Context, currentEtag string) (ConfigSnapshot, error)

// ErrUnsupported is the sentinel an adapter returns when a method is
// called whose Capabilities bit is unset. Engine code should match it
// with errors.Is and treat it as a fast no-op, not a failure.
var ErrUnsupported = errors.New("clients: operation not supported by this protocol adapter")
