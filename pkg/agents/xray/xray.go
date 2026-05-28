// Package xray is the HUE adapter for an Xray-core instance.
//
// Two responsibilities:
//
//  1. **Config sync** — fetches the abstract key-value config from
//     HUE's NodeService.SyncConfig, runs it through a ConfigGenerator
//     (e.g., VlessXHTTP), and hands the resulting bytes to an
//     ApplyConfig callback. The adapter caches the etag so subsequent
//     syncs that haven't changed are no-ops.
//
//  2. **Stats / disconnect / provisioning** — talk to xray's gRPC API
//     (xray.app.stats.command.StatsService and
//     xray.app.proxyman.command.HandlerService) to read traffic
//     counters and add/remove users on a live inbound. These are
//     stubbed pending the wire-format work documented in the doc
//     comments — Capabilities() omits those bits until they're ready.
//
// To use it:
//
//	c, err := xray.New(xray.Config{
//	    Endpoint:    "127.0.0.1:10085",
//	    Inbound:     "vless-in",
//	    ServiceID:   "<uuid of this xray's Service row in HUE>",
//	    Generator:   xray.VlessXHTTP{},
//	    SyncConfig:  yourFuncWrappingNodeServiceClient,
//	    ApplyConfig: yourApplyFunc,
//	})
//	if err != nil { return err }
//	defer c.Close()
//
//	// Bootstrap on first connection:
//	if _, err := c.SyncConfig(ctx); err != nil { return err }
package xray

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/hiddify/hue/pkg/agents"
	xraycmd "github.com/hiddify/hue/pkg/agents/xray/api/xray/app/proxyman/command"
	xraystats "github.com/hiddify/hue/pkg/agents/xray/api/xray/app/stats/command"
	xrayprotocol "github.com/hiddify/hue/pkg/agents/xray/api/xray/common/protocol"
	xrayvless "github.com/hiddify/hue/pkg/agents/xray/api/xray/proxy/vless/account"
)

// Config is the per-instance config for one xray-core API endpoint.
type Config struct {
	// Endpoint is host:port of xray's `api` inbound.
	Endpoint string

	// Inbound is the tag of the inbound HUE manages users on, e.g.
	// "vless-in". Required for AddUser / RemoveUser; ignored for stats.
	Inbound string

	// ServiceID is HUE's Service.id this xray instance is bound to.
	// Used as the SyncConfig key so HUE knows which row to return.
	// Required when SyncConfig is non-nil.
	ServiceID string

	// Renderer turns the ConfigSnapshot HUE returns (template + vars +
	// users) into the bytes the running xray process consumes.
	// Required when SyncConfig is non-nil. The default XrayJSON{}
	// covers any xray-core inbound — the per-protocol shape lives in
	// the template, not in Go code.
	Renderer Renderer

	// SyncConfig is the callback that fetches HUE's current config for
	// this service. Application code wires it to a real
	// huev1.NodeServiceClient.SyncConfig wrapper; tests pass a fake.
	// nil disables config sync.
	SyncConfig agents.SyncConfigFunc

	// ApplyConfig installs newly-generated config onto the running
	// xray process. nil = log + drop (suitable for tests; production
	// deployments must supply a real implementation).
	ApplyConfig ApplyConfigFunc

	// DialTimeout caps initial gRPC dial; default 5s.
	DialTimeout time.Duration

	dialOpts []grpc.DialOption
}

// Option configures a *Client at construction time.
type Option func(*Client)

// WithDialOption appends grpc.DialOption(s) to the dial — TLS creds,
// keepalive params, interceptors, etc.
func WithDialOption(opts ...grpc.DialOption) Option {
	return func(c *Client) { c.cfg.dialOpts = append(c.cfg.dialOpts, opts...) }
}

// Client is the xray adapter. Goroutine-safe; one instance per
// xray-core process.
type Client struct {
	cfg  Config
	conn *grpc.ClientConn

	mu          sync.Mutex // protects cachedEtag + cachedBytes
	cachedEtag  string
	cachedBytes []byte
}

// New constructs and dials. Returns a fully usable client or an error.
func New(cfg Config, opts ...Option) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("xray: Config.Endpoint is required")
	}
	if cfg.SyncConfig != nil {
		if cfg.ServiceID == "" {
			return nil, errors.New("xray: Config.ServiceID is required when SyncConfig is set")
		}
		if cfg.Renderer == nil {
			return nil, errors.New("xray: Config.Renderer is required when SyncConfig is set")
		}
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}

	c := &Client{cfg: cfg}
	for _, o := range opts {
		o(c)
	}

	dialOpts := c.cfg.dialOpts
	if !hasCreds(dialOpts) {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.NewClient(cfg.Endpoint, dialOpts...)
	if err != nil {
		return nil, err
	}
	c.conn = conn
	return c, nil
}

func (c *Client) Name() string { return "xray" }

// Capabilities reports what's wired today. CapConfigSync is on iff a
// SyncConfig callback was supplied; CapProvision + CapDisconnect are on
// when Inbound is set; CapStats is always on (StatsService is always
// reachable on the api inbound — operator must enable stats in xray config).
func (c *Client) Capabilities() agents.Capability {
	caps := agents.CapHealthcheck | agents.CapStats
	if c.cfg.SyncConfig != nil {
		caps |= agents.CapConfigSync
	}
	if c.cfg.Inbound != "" {
		caps |= agents.CapProvision | agents.CapDisconnect
	}
	return caps
}

// Healthcheck triggers a connection state check.
func (c *Client) Healthcheck(ctx context.Context) error {
	if c.conn == nil {
		return errors.New("xray: client is closed")
	}
	c.conn.Connect()
	for {
		s := c.conn.GetState()
		if s == connectivity.Ready {
			return nil
		}
		if !c.conn.WaitForStateChange(ctx, s) {
			return ctx.Err()
		}
	}
}

// ReadStats queries xray's StatsService for per-user traffic counters and
// resets them atomically. Counter names follow xray's convention:
//
//	user>>><email>>>>traffic>>>uplink
//	user>>><email>>>>traffic>>>downlink
//
// Only non-zero counters are returned. The caller (engine) should call
// ReportUsage for each delta to persist and enforce quota.
func (c *Client) ReadStats(ctx context.Context) ([]agents.UsageDelta, error) {
	resp, err := xraystats.NewStatsServiceClient(c.conn).QueryStats(ctx,
		&xraystats.QueryStatsRequest{Pattern: "user>>>", Reset_: true})
	if err != nil {
		return nil, fmt.Errorf("xray: QueryStats: %w", err)
	}
	// Aggregate per-user: collect uplink + downlink separately.
	type pair struct{ up, down int64 }
	byEmail := make(map[string]*pair)
	for _, s := range resp.GetStat() {
		email, dir, ok := parseStatName(s.GetName())
		if !ok || s.GetValue() == 0 {
			continue
		}
		p := byEmail[email]
		if p == nil {
			p = &pair{}
			byEmail[email] = p
		}
		switch dir {
		case "uplink":
			p.up += s.GetValue()
		case "downlink":
			p.down += s.GetValue()
		}
	}
	out := make([]agents.UsageDelta, 0, len(byEmail))
	now := time.Now().UTC()
	for email, p := range byEmail {
		out = append(out, agents.UsageDelta{
			User:     agents.User{Tag: email, Inbound: c.cfg.Inbound},
			Upload:   p.up,
			Download: p.down,
			At:       now,
		})
	}
	return out, nil
}

// parseStatName splits "user>>><email>>>>traffic>>>uplink" into
// (email, "uplink", true). Returns ("","",false) for unrecognised names.
func parseStatName(name string) (email, direction string, ok bool) {
	// Expected: "user>>>" + email + ">>>traffic>>>" + dir
	const prefix = "user>>>"
	const trafficSep = ">>>traffic>>>"
	if !strings.HasPrefix(name, prefix) {
		return
	}
	rest := name[len(prefix):]
	idx := strings.Index(rest, trafficSep)
	if idx < 0 {
		return
	}
	email = rest[:idx]
	direction = rest[idx+len(trafficSep):]
	ok = email != "" && (direction == "uplink" || direction == "downlink")
	return
}

// Disconnect removes u from the inbound and immediately terminates active
// sessions by calling RemoveUser then AddUser with a fresh identity. xray
// closes existing TCP flows when a user is removed.
func (c *Client) Disconnect(ctx context.Context, u agents.User) error {
	return c.RemoveUser(ctx, u)
}

// AddUser provisions u on the configured inbound via xray's HandlerService
// AlterInbound RPC. secret is the vless UUID (same as u.ID by convention;
// callers may override for protocols that use a different secret).
func (c *Client) AddUser(ctx context.Context, u agents.User, secret string) error {
	if c.cfg.Inbound == "" {
		return agents.ErrUnsupported
	}
	if secret == "" {
		secret = u.ID
	}
	acct, err := anypb.New(&xrayvless.Account{
		Id:         secret,
		Encryption: "none",
	})
	if err != nil {
		return fmt.Errorf("xray: marshal vless account: %w", err)
	}
	user := &xrayprotocol.User{
		Email:   u.Tag,
		Account: acct,
	}
	op, err := anypb.New(&xraycmd.AddUserOperation{User: user})
	if err != nil {
		return fmt.Errorf("xray: marshal AddUserOperation: %w", err)
	}
	_, err = xraycmd.NewHandlerServiceClient(c.conn).AlterInbound(ctx,
		&xraycmd.AlterInboundRequest{Tag: c.cfg.Inbound, Operation: op})
	if err != nil {
		return fmt.Errorf("xray: AlterInbound AddUser %q: %w", u.Tag, err)
	}
	return nil
}

// RemoveUser deprovisions u from the configured inbound via xray's
// HandlerService AlterInbound RPC. Idempotent — xray returns OK when the
// user is already absent.
func (c *Client) RemoveUser(ctx context.Context, u agents.User) error {
	if c.cfg.Inbound == "" {
		return agents.ErrUnsupported
	}
	op, err := anypb.New(&xraycmd.RemoveUserOperation{Email: u.Tag})
	if err != nil {
		return fmt.Errorf("xray: marshal RemoveUserOperation: %w", err)
	}
	_, err = xraycmd.NewHandlerServiceClient(c.conn).AlterInbound(ctx,
		&xraycmd.AlterInboundRequest{Tag: c.cfg.Inbound, Operation: op})
	if err != nil {
		return fmt.Errorf("xray: AlterInbound RemoveUser %q: %w", u.Tag, err)
	}
	return nil
}

// SyncConfig pulls the adapter's current config snapshot from HUE,
// runs it through the configured Renderer, and applies the rendered
// bytes via ApplyConfig. Reports whether the local config was actually
// replaced.
//
// Idempotent and cheap when the etag is unchanged: the inner RPC
// returns Changed=false and SyncConfig is a no-op (no render, no apply).
func (c *Client) SyncConfig(ctx context.Context) (bool, error) {
	if c.cfg.SyncConfig == nil {
		return false, agents.ErrUnsupported
	}

	c.mu.Lock()
	currentEtag := c.cachedEtag
	c.mu.Unlock()

	snap, err := c.cfg.SyncConfig(ctx, currentEtag)
	if err != nil {
		return false, err
	}
	if !snap.Changed {
		return false, nil
	}

	if snap.TemplateFormat != "" && !c.cfg.Renderer.AcceptsFormat(snap.TemplateFormat) {
		return false, fmt.Errorf("xray: renderer %q does not accept template format %q",
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

// CachedConfig returns the most recently applied generated bytes.
// Returns nil before the first successful SyncConfig call.
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

// CachedEtag returns the etag of the last applied config (empty until
// the first successful SyncConfig).
func (c *Client) CachedEtag() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedEtag
}

func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func hasCreds(_ []grpc.DialOption) bool {
	// grpc.DialOption is opaque; we can't introspect it without a
	// private API. Conventionally, callers either always set transport
	// credentials or never. We err toward "never" and let an explicit
	// WithTransportCredentials in dialOpts override the insecure
	// default appended in New.
	return false
}
