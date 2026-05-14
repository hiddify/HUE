package wireguard_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hiddify/hue/pkg/agents"
	"github.com/hiddify/hue/pkg/agents/wireguard"
)

var _ agents.Agent = (*wireguard.Client)(nil)
var _ wireguard.Renderer = wireguard.WGQuickRenderer{}

func TestWG_NewBareNeedsNothing(t *testing.T) {
	t.Parallel()
	if _, err := wireguard.New(wireguard.Config{}); err != nil {
		t.Fatalf("bare New: %v", err)
	}
}

func TestWG_NewWithSyncConfigRequiresInterface(t *testing.T) {
	t.Parallel()
	_, err := wireguard.New(wireguard.Config{
		ServiceID:  "svc",
		SyncConfig: stub(agents.ConfigSnapshot{}, nil),
	})
	if err == nil || !strings.Contains(err.Error(), "Interface") {
		t.Fatalf("expected Interface error, got %v", err)
	}
}

func TestWG_SyncConfigRendersInterfaceAndPeers(t *testing.T) {
	t.Parallel()
	tpl := `[Interface]
PrivateKey = {{getVar "wg.private_key" ""}}
ListenPort = {{getVar "wg.port" "51820"}}

{{range $i, $u := .Users}}
[Peer]
PublicKey  = {{$u.PublicKey}}
AllowedIPs = 10.0.0.{{$i}}/32
{{end}}`
	snap := agents.ConfigSnapshot{
		Template: tpl,
		Vars: map[string]string{
			"wg.private_key": "PRIVATE_KEY_PLACEHOLDER",
			"wg.port":        "51820",
		},
		Users: []agents.ConfigUser{
			{ID: "alice", PublicKey: "ALICE_PUBKEY"},
			{ID: "bob", PublicKey: "BOB_PUBKEY"},
		},
		Etag:    "e1",
		Changed: true,
	}
	var called atomic.Int32
	var rendered []byte
	c, _ := wireguard.New(wireguard.Config{
		Interface:   "wg0",
		ServiceID:   "svc",
		Renderer:    wireguard.WGQuickRenderer{},
		SyncConfig:  stub(snap, nil),
		ApplyConfig: func(b []byte) error { called.Add(1); rendered = b; return nil },
	})
	defer c.Close()
	changed, err := c.SyncConfig(context.Background())
	if err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if called.Load() != 1 {
		t.Fatalf("ApplyConfig called %d times, want 1", called.Load())
	}
	got := string(rendered)
	for _, want := range []string{
		"PrivateKey = PRIVATE_KEY_PLACEHOLDER",
		"ListenPort = 51820",
		"PublicKey  = ALICE_PUBKEY",
		"PublicKey  = BOB_PUBKEY",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q.\n--- output ---\n%s", want, got)
		}
	}
}

func stub(snap agents.ConfigSnapshot, err error) agents.SyncConfigFunc {
	return func(context.Context, string) (agents.ConfigSnapshot, error) {
		return snap, err
	}
}
