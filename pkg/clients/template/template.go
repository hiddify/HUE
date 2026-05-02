// Package template is the copy-paste starting point for a new HUE
// protocol adapter.
//
// To add support for a new protocol "foo":
//
//  1. cp -r pkg/clients/template pkg/clients/foo
//  2. Replace every occurrence of "template" / "Template" with
//     "foo" / "Foo" (in package paths, type names, and the Name()
//     return value).
//  3. Fill Config with the fields the protocol's admin API needs
//     (endpoint, API key, mTLS material, …).
//  4. Pick which Capabilities the adapter actually supports and OR
//     them in Capabilities(); leave the rest as ErrUnsupported.
//  5. Implement the picked methods. Keep package-internal types in
//     this package; only the clients.Client surface should leak.
//  6. Write a sibling _test.go that fakes the protocol's wire format
//     so unit tests don't need a live service.
//
// The shape this package gives you is intentionally minimal — under
// 100 lines, no third-party dependencies — so you can fork it without
// inheriting things you don't need.
package template

import (
	"context"
	"errors"

	"github.com/hiddify/hue/pkg/clients"
)

// Config carries everything the adapter needs to talk to a single
// instance of the service. Keep it pure data — no network calls in
// New until the caller is ready.
type Config struct {
	Endpoint string // host:port the service's admin API listens on
	APIKey   string // bearer token / shared secret, if any
	// Add: TLS certs, inbound name, namespace, … as the protocol demands.
}

// Client is the adapter. Only this type and Config are exported from
// the package; everything else is an implementation detail.
type Client struct {
	cfg Config
	// Add: gRPC conn, http.Client, *exec.Cmd, …
}

// New constructs the adapter without dialing. Returning here lets tests
// inject fakes via interface assertions before any network I/O happens.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("template: Config.Endpoint is required")
	}
	return &Client{cfg: cfg}, nil
}

// Name is the protocol identifier used in metrics + logs.
func (c *Client) Name() string { return "template" }

// Capabilities reports what this adapter implements. The empty
// template returns 0 — the engine treats that as "do not call any
// method on this client" and the registration is a no-op.
func (c *Client) Capabilities() clients.Capability {
	return 0
	// Real adapter would OR the supported bits, e.g.:
	// return clients.CapHealthcheck | clients.CapStats | clients.CapDisconnect
}

func (c *Client) Healthcheck(_ context.Context) error {
	return clients.ErrUnsupported
}

func (c *Client) ReadStats(_ context.Context) ([]clients.UsageDelta, error) {
	return nil, clients.ErrUnsupported
}

func (c *Client) Disconnect(_ context.Context, _ clients.User) error {
	return clients.ErrUnsupported
}

func (c *Client) AddUser(_ context.Context, _ clients.User, _ string) error {
	return clients.ErrUnsupported
}

func (c *Client) RemoveUser(_ context.Context, _ clients.User) error {
	return clients.ErrUnsupported
}

func (c *Client) SyncConfig(_ context.Context) (bool, error) {
	return false, clients.ErrUnsupported
}

// Close releases adapter resources. Safe to call multiple times.
func (c *Client) Close() error { return nil }
