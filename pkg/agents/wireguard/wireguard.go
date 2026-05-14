// Package wireguard is the HUE adapter for a wg-quick interface.
//
// Phase 2.6 ships the skeleton: dial-equivalent (the interface name)
// and the renderer that turns a HUE ConfigSnapshot into a wg-quick
// .conf file. Apply (write to /etc/wireguard/<iface>.conf + `wg
// syncconf`) is the caller's ApplyConfig.
//
// Stats / Disconnect / AddClient / RemoveClient via `wg show` /
// `wg set` are stubbed; lands in 2.7's E2E suite when the full
// life-cycle gets tested against a real wg-go binary.
package wireguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"text/template"

	"github.com/hiddify/hue/pkg/agents"
)

// Config is the per-instance config for one wg interface.
type Config struct {
	// Interface is the wg name (e.g. "wg0"). Required when SyncConfig
	// is set.
	Interface string

	// ServiceID is HUE's Agent.id this wg adapter is bound to.
	// Required when SyncConfig is non-nil. (Field name kept as
	// ServiceID to match the xray adapter's shape — both are an
	// agent's identifier from HUE's perspective.)
	ServiceID string

	// Renderer turns the HUE ConfigSnapshot into wg-quick ini bytes.
	// Default WGQuickRenderer{} covers the common case; custom
	// renderers can be supplied for non-standard transports.
	Renderer Renderer

	// SyncConfig fetches the snapshot from HUE.
	SyncConfig agents.SyncConfigFunc

	// ApplyConfig installs the rendered bytes. Typical:
	//   write to /etc/wireguard/wg0.conf + run `wg syncconf wg0 <(wg-quick strip wg0)`
	ApplyConfig ApplyConfigFunc
}

// ApplyConfigFunc — same shape as the xray adapter's.
type ApplyConfigFunc func(generated []byte) error

// Renderer is the wg-quick equivalent of xray's Renderer.
type Renderer interface {
	Name() string
	AcceptsFormat(format string) bool
	Render(snap agents.ConfigSnapshot) ([]byte, error)
}

// Client is the wireguard adapter.
type Client struct {
	cfg Config

	mu          sync.Mutex
	cachedEtag  string
	cachedBytes []byte
}

// New constructs the adapter. Does NOT dial wg — that's the
// ApplyConfig step's job. Returns a usable Client or error.
func New(cfg Config) (*Client, error) {
	if cfg.SyncConfig != nil {
		if cfg.Interface == "" {
			return nil, errors.New("wireguard: Config.Interface is required when SyncConfig is set")
		}
		if cfg.ServiceID == "" {
			return nil, errors.New("wireguard: Config.ServiceID is required when SyncConfig is set")
		}
		if cfg.Renderer == nil {
			cfg.Renderer = WGQuickRenderer{}
		}
	}
	return &Client{cfg: cfg}, nil
}

func (c *Client) Name() string { return "wireguard" }

func (c *Client) Capabilities() agents.Capability {
	caps := agents.CapHealthcheck
	if c.cfg.SyncConfig != nil {
		caps |= agents.CapConfigSync
	}
	return caps
	// CapStats / CapDisconnect / CapProvision land in 2.7 against a
	// real wg userspace binary.
}

func (c *Client) Healthcheck(_ context.Context) error {
	// Phase 2.6: trivially OK (the operator owns the wg lifecycle).
	// Real check = `wg show <iface>` exec — added in 2.7.
	return nil
}

func (c *Client) ReadStats(_ context.Context) ([]agents.UsageDelta, error) {
	return nil, agents.ErrUnsupported
}

func (c *Client) Disconnect(_ context.Context, _ agents.User) error {
	return agents.ErrUnsupported
}

func (c *Client) AddUser(_ context.Context, _ agents.User, _ string) error {
	return agents.ErrUnsupported
}

func (c *Client) RemoveUser(_ context.Context, _ agents.User) error {
	return agents.ErrUnsupported
}

// SyncConfig pulls the snapshot from HUE, renders, applies.
func (c *Client) SyncConfig(ctx context.Context) (bool, error) {
	if c.cfg.SyncConfig == nil {
		return false, agents.ErrUnsupported
	}
	c.mu.Lock()
	cur := c.cachedEtag
	c.mu.Unlock()

	snap, err := c.cfg.SyncConfig(ctx, cur)
	if err != nil {
		return false, err
	}
	if !snap.Changed {
		return false, nil
	}
	if snap.TemplateFormat != "" && !c.cfg.Renderer.AcceptsFormat(snap.TemplateFormat) {
		return false, fmt.Errorf("wireguard: renderer %q does not accept %q",
			c.cfg.Renderer.Name(), snap.TemplateFormat)
	}
	rendered, err := c.cfg.Renderer.Render(snap)
	if err != nil {
		return false, err
	}
	if c.cfg.ApplyConfig != nil {
		if err := c.cfg.ApplyConfig(rendered); err != nil {
			return false, err
		}
	}
	c.mu.Lock()
	c.cachedEtag = snap.Etag
	c.cachedBytes = rendered
	c.mu.Unlock()
	return true, nil
}

func (c *Client) Close() error { return nil }

func (c *Client) CachedConfig() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedBytes == nil {
		return nil
	}
	cp := make([]byte, len(c.cachedBytes))
	copy(cp, c.cachedBytes)
	return cp
}

func (c *Client) CachedEtag() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedEtag
}

// ----------------------------------------------------------------------
// WGQuickRenderer — text/template renderer producing wg-quick ini.
// ----------------------------------------------------------------------

// WGQuickRenderer renders a wg-quick .conf from a HUE ConfigSnapshot.
// Template author writes the [Interface] block + iterates over .Users
// to emit [Peer] sections, using .PublicKey + allowed groups.
//
// Example template (lives in Node.config under wireguard.numeric_version.N):
//
//   [Interface]
//   PrivateKey = {{getVar "wg.interface.private_key" ""}}
//   ListenPort = {{getVar "wg.interface.listen_port" "51820"}}
//   Address    = {{getVar "wg.interface.address" "10.0.0.1/24"}}
//
//   {{range .Users}}
//   [Peer]
//   PublicKey  = {{.PublicKey}}
//   AllowedIPs = 10.0.0.{{.ID}}/32     # or whatever assignment scheme
//   {{end}}
type WGQuickRenderer struct{}

func (WGQuickRenderer) Name() string { return "wireguard-ini" }

func (WGQuickRenderer) AcceptsFormat(f string) bool {
	return f == "" || f == "wireguard-ini"
}

func (WGQuickRenderer) Render(snap agents.ConfigSnapshot) ([]byte, error) {
	if snap.Template == "" {
		return nil, errors.New("wireguard: empty config template")
	}
	t, err := template.New("wg-quick").
		Option("missingkey=error").
		Funcs(template.FuncMap{
			"getVar": func(k, d string) string {
				if v, ok := snap.Vars[k]; ok && v != "" {
					return v
				}
				return d
			},
		}).
		Parse(snap.Template)
	if err != nil {
		return nil, fmt.Errorf("wireguard: parse template: %w", err)
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, struct {
		Vars  map[string]string
		Users []agents.ConfigUser
	}{snap.Vars, snap.Users})
	if err != nil {
		return nil, fmt.Errorf("wireguard: execute template: %w", err)
	}
	return buf.Bytes(), nil
}
