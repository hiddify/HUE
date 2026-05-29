// Package radius is the HUE adapter for a RADIUS authentication / accounting
// service.
//
// # Status
//
// This is a compilable scaffold. The UDP listener, packet codec, and
// accounting-to-ReportUsage path are stubs — see the TODO comments.
// To harden this into production code:
//
//  1. Add a RADIUS codec dependency (e.g. layeh.com/radius) to go.mod.
//  2. Implement the UDP listener in listen().
//  3. Implement Access-Request handling in handleAuth(): look up the
//     username in c.snapshot.Users, verify the PAP password, return
//     Access-Accept or Access-Reject.
//  4. Implement Accounting-Request handling in handleAccounting(): parse
//     bytes sent/received and call SyncConfig/ReportUsage.
//  5. Implement ReadStats to return usage accumulated from accounting.
//  6. Raise Capabilities() to include CapStats | CapProvision.
//
// # Wire
//
// RADIUS RFC 2865 (authentication, port 1812/UDP) +
// RADIUS RFC 2866 (accounting,     port 1813/UDP).
//
// # Config
//
// All fields are read from environment variables via the operator's
// deployment tooling. The agent itself is constructed programmatically;
// there is no flag parsing inside this package.
package radius

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/hiddify/hue/pkg/agents"
)

// Config is per-instance configuration for one RADIUS endpoint.
type Config struct {
	// AuthAddr is the UDP address to listen on for Access-Request packets.
	// Default "0.0.0.0:1812".
	AuthAddr string

	// AcctAddr is the UDP address to listen on for Accounting-Request packets.
	// Default "0.0.0.0:1813". Set empty to disable accounting.
	AcctAddr string

	// SharedSecret is the RADIUS shared secret negotiated with NAS devices.
	// Required — must not be empty.
	SharedSecret string

	// SyncConfig is the callback that fetches HUE's current config snapshot
	// (user list + shared config). Wire to a ConfigServiceClient wrapper.
	SyncConfig agents.SyncConfigFunc
}

// Client is the RADIUS adapter. Goroutine-safe.
type Client struct {
	cfg Config

	mu       sync.Mutex
	snapshot agents.ConfigSnapshot

	authConn *net.UDPConn
	acctConn *net.UDPConn

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

// New constructs the adapter and validates config. Does not start
// listening; call Listen to open UDP sockets.
func New(cfg Config) (*Client, error) {
	if cfg.SharedSecret == "" {
		return nil, errors.New("radius: Config.SharedSecret is required")
	}
	if cfg.AuthAddr == "" {
		cfg.AuthAddr = "0.0.0.0:1812"
	}
	if cfg.AcctAddr == "" {
		cfg.AcctAddr = "0.0.0.0:1813"
	}
	return &Client{
		cfg:  cfg,
		stop: make(chan struct{}),
	}, nil
}

// Name is the stable protocol identifier.
func (c *Client) Name() string { return "radius" }

// Capabilities reports what is implemented today. CapConfigSync is on
// when a SyncConfig callback was supplied. CapProvision, CapStats, and
// CapDisconnect are pending the stub implementations above.
func (c *Client) Capabilities() agents.Capability {
	var caps agents.Capability
	caps |= agents.CapHealthcheck
	if c.cfg.SyncConfig != nil {
		caps |= agents.CapConfigSync
	}
	// TODO: add CapStats | CapProvision | CapDisconnect once implemented.
	return caps
}

// Healthcheck reports whether the UDP auth socket is open.
func (c *Client) Healthcheck(_ context.Context) error {
	c.mu.Lock()
	conn := c.authConn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("radius: auth socket not open — call Listen first")
	}
	return nil
}

// ReadStats returns accumulated traffic deltas from accounting packets.
// Stub — returns ErrUnsupported until accounting is implemented.
func (c *Client) ReadStats(_ context.Context) ([]agents.UsageDelta, error) {
	return nil, agents.ErrUnsupported
}

// Disconnect is not yet implemented for RADIUS.
func (c *Client) Disconnect(_ context.Context, _ agents.User) error {
	return agents.ErrUnsupported
}

// AddUser provisions a user in the local snapshot cache so
// subsequent Access-Requests can accept them without a full sync.
// Stub — the snapshot is managed by SyncConfig.
func (c *Client) AddUser(_ context.Context, _ agents.User, _ string) error {
	return agents.ErrUnsupported
}

// RemoveUser removes a user from the local snapshot cache.
// Stub — the snapshot is managed by SyncConfig.
func (c *Client) RemoveUser(_ context.Context, _ agents.User) error {
	return agents.ErrUnsupported
}

// SyncConfig pulls fresh user + config data from HUE and caches it
// locally. The RADIUS server uses this snapshot to validate
// Access-Requests without a per-packet round-trip to HUE.
func (c *Client) SyncConfig(ctx context.Context) (bool, error) {
	if c.cfg.SyncConfig == nil {
		return false, agents.ErrUnsupported
	}
	c.mu.Lock()
	etag := c.snapshot.Etag
	c.mu.Unlock()

	snap, err := c.cfg.SyncConfig(ctx, etag)
	if err != nil {
		return false, err
	}
	if !snap.Changed {
		return false, nil
	}
	c.mu.Lock()
	c.snapshot = snap
	c.mu.Unlock()
	return true, nil
}

// Listen opens the UDP sockets and starts background goroutines to
// process packets. Call Close to stop them.
//
// TODO: replace the stub UDP loops with a real RADIUS codec once a
// dependency (e.g. layeh.com/radius) is added to go.mod.
func (c *Client) Listen() error {
	authAddr, err := net.ResolveUDPAddr("udp", c.cfg.AuthAddr)
	if err != nil {
		return err
	}
	authConn, err := net.ListenUDP("udp", authAddr)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.authConn = authConn
	c.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.listenUDP(authConn, c.handleAuth)
	}()

	if c.cfg.AcctAddr != "" {
		acctAddr, err := net.ResolveUDPAddr("udp", c.cfg.AcctAddr)
		if err != nil {
			_ = authConn.Close()
			return err
		}
		acctConn, err := net.ListenUDP("udp", acctAddr)
		if err != nil {
			_ = authConn.Close()
			return err
		}
		c.mu.Lock()
		c.acctConn = acctConn
		c.mu.Unlock()
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.listenUDP(acctConn, c.handleAccounting)
		}()
	}

	return nil
}

// Close stops the UDP listeners and waits for goroutines to exit.
func (c *Client) Close() error {
	c.stopOnce.Do(func() { close(c.stop) })
	c.mu.Lock()
	ac := c.authConn
	cc := c.acctConn
	c.mu.Unlock()
	if ac != nil {
		_ = ac.Close()
	}
	if cc != nil {
		_ = cc.Close()
	}
	c.wg.Wait()
	return nil
}

// listenUDP reads raw UDP packets and dispatches to handler until the
// connection is closed or stop is signalled.
func (c *Client) listenUDP(conn *net.UDPConn, handler func([]byte, *net.UDPAddr)) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // conn was closed
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		go handler(pkt, addr)
	}
}

// handleAuth processes a raw Access-Request packet.
//
// TODO: decode the RADIUS packet using a codec, look up the username in
// c.snapshot.Users, verify the PAP User-Password attribute against the
// cached plaintext password, and write an Access-Accept or
// Access-Reject back to addr.
func (c *Client) handleAuth(_ []byte, _ *net.UDPAddr) {
	// stub
}

// handleAccounting processes a raw Accounting-Request packet.
//
// TODO: decode Acct-Input-Octets / Acct-Output-Octets / Acct-Session-Id
// and either buffer deltas for ReadStats or call ReportUsage directly.
func (c *Client) handleAccounting(_ []byte, _ *net.UDPAddr) {
	// stub
}
