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

// SyncConfigFunc is the callback an adapter uses to fetch the current
// config from HUE. The contract:
//
//   - currentEtag: the etag the adapter received last time (empty on
//     first call).
//   - returned kv:    the current config (empty map when changed=false).
//   - returned etag:  what the adapter should store and pass on the
//     next call.
//   - returned changed: true if kv must be (re-)applied; false means
//     the local copy is already up to date.
//
// Application code wires this to a real huev1.NodeServiceClient.
// Tests inject a fake. Keeping the adapter free of huev1 imports keeps
// the public package tree light.
type SyncConfigFunc func(ctx context.Context, currentEtag string) (
	kv map[string]string,
	etag string,
	changed bool,
	err error,
)

// ErrUnsupported is the sentinel an adapter returns when a method is
// called whose Capabilities bit is unset. Engine code should match it
// with errors.Is and treat it as a fast no-op, not a failure.
var ErrUnsupported = errors.New("clients: operation not supported by this protocol adapter")
