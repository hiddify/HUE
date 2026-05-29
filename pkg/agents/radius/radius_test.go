package radius

import (
	"context"
	"testing"

	"github.com/hiddify/hue/pkg/agents"
)

func TestNew_RequiresSharedSecret(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("expected error when SharedSecret is empty")
	}
}

func TestNew_DefaultAddrs(t *testing.T) {
	c, err := New(Config{SharedSecret: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	if c.cfg.AuthAddr != "0.0.0.0:1812" {
		t.Errorf("AuthAddr = %q, want 0.0.0.0:1812", c.cfg.AuthAddr)
	}
	if c.cfg.AcctAddr != "0.0.0.0:1813" {
		t.Errorf("AcctAddr = %q, want 0.0.0.0:1813", c.cfg.AcctAddr)
	}
}

func TestName(t *testing.T) {
	c, _ := New(Config{SharedSecret: "s"})
	if c.Name() != "radius" {
		t.Errorf("Name = %q, want radius", c.Name())
	}
}

func TestCapabilities_NoSyncConfig(t *testing.T) {
	c, _ := New(Config{SharedSecret: "s"})
	caps := c.Capabilities()
	if !caps.Has(agents.CapHealthcheck) {
		t.Error("expected CapHealthcheck")
	}
	if caps.Has(agents.CapConfigSync) {
		t.Error("CapConfigSync should be absent when SyncConfig is nil")
	}
}

func TestCapabilities_WithSyncConfig(t *testing.T) {
	c, _ := New(Config{
		SharedSecret: "s",
		SyncConfig: func(_ context.Context, _ string) (agents.ConfigSnapshot, error) {
			return agents.ConfigSnapshot{}, nil
		},
	})
	caps := c.Capabilities()
	if !caps.Has(agents.CapConfigSync) {
		t.Error("expected CapConfigSync when SyncConfig is set")
	}
}

func TestHealthcheck_BeforeListen(t *testing.T) {
	c, _ := New(Config{SharedSecret: "s"})
	err := c.Healthcheck(context.Background())
	if err == nil {
		t.Fatal("expected error before Listen is called")
	}
}

func TestUnsupportedMethods(t *testing.T) {
	c, _ := New(Config{SharedSecret: "s"})
	ctx := context.Background()
	u := agents.User{ID: "x", Tag: "x"}

	if _, err := c.ReadStats(ctx); err != agents.ErrUnsupported {
		t.Errorf("ReadStats: got %v, want ErrUnsupported", err)
	}
	if err := c.Disconnect(ctx, u); err != agents.ErrUnsupported {
		t.Errorf("Disconnect: got %v, want ErrUnsupported", err)
	}
	if err := c.AddUser(ctx, u, ""); err != agents.ErrUnsupported {
		t.Errorf("AddUser: got %v, want ErrUnsupported", err)
	}
	if err := c.RemoveUser(ctx, u); err != agents.ErrUnsupported {
		t.Errorf("RemoveUser: got %v, want ErrUnsupported", err)
	}
	if _, err := c.SyncConfig(ctx); err != agents.ErrUnsupported {
		t.Errorf("SyncConfig without callback: got %v, want ErrUnsupported", err)
	}
}

func TestSyncConfig_ChangedFalseWhenSameEtag(t *testing.T) {
	c, _ := New(Config{
		SharedSecret: "s",
		SyncConfig: func(_ context.Context, etag string) (agents.ConfigSnapshot, error) {
			// Simulate no change.
			return agents.ConfigSnapshot{Changed: false, Etag: etag}, nil
		},
	})
	changed, err := c.SyncConfig(context.Background())
	if err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	if changed {
		t.Error("expected changed=false when snapshot unchanged")
	}
}

func TestSyncConfig_UpdatesSnapshot(t *testing.T) {
	snap := agents.ConfigSnapshot{
		Changed: true,
		Etag:    "abc",
		Users: []agents.ConfigUser{
			{ID: "u1", Username: "alice"},
		},
	}
	c, _ := New(Config{
		SharedSecret: "s",
		SyncConfig: func(_ context.Context, _ string) (agents.ConfigSnapshot, error) {
			return snap, nil
		},
	})
	changed, err := c.SyncConfig(context.Background())
	if err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	if !changed {
		t.Error("expected changed=true on first sync")
	}
	c.mu.Lock()
	got := c.snapshot
	c.mu.Unlock()
	if got.Etag != "abc" {
		t.Errorf("snapshot etag = %q, want abc", got.Etag)
	}
	if len(got.Users) != 1 || got.Users[0].Username != "alice" {
		t.Errorf("snapshot users = %v, want [{alice}]", got.Users)
	}
}

func TestListen_BindsAndCloses(t *testing.T) {
	c, err := New(Config{
		SharedSecret: "s",
		AuthAddr:     "127.0.0.1:0", // random free port
		AcctAddr:     "",             // accounting disabled
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// Healthcheck should pass after Listen.
	if err := c.Healthcheck(context.Background()); err != nil {
		t.Errorf("Healthcheck after Listen: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
