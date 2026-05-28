package xray_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/hiddify/hue/pkg/agents"
	"github.com/hiddify/hue/pkg/agents/xray"
	xraycmd "github.com/hiddify/hue/pkg/agents/xray/api/xray/app/proxyman/command"
	xraystats "github.com/hiddify/hue/pkg/agents/xray/api/xray/app/stats/command"
)

// Compile-time assertion: the xray adapter implements agents.Agent.
var _ agents.Agent = (*xray.Client)(nil)

// Compile-time assertion: XrayJSON implements xray.Renderer.
var _ xray.Renderer = xray.XrayJSON{}

func TestXray_NewRejectsEmptyEndpoint(t *testing.T) {
	t.Parallel()
	if _, err := xray.New(xray.Config{}); err == nil {
		t.Error("New must reject an empty Endpoint")
	}
}

func TestXray_NewRequiresRendererWhenSyncConfigSet(t *testing.T) {
	t.Parallel()
	_, err := xray.New(xray.Config{
		Endpoint:   "127.0.0.1:0",
		ServiceID:  "svc-1",
		SyncConfig: stubSyncConfig(agents.ConfigSnapshot{Changed: true}, nil),
	})
	if err == nil || !strings.Contains(err.Error(), "Renderer") {
		t.Fatalf("expected error about missing Renderer, got %v", err)
	}
}

func TestXray_CapabilitiesReflectWiring(t *testing.T) {
	t.Parallel()

	bare, err := xray.New(xray.Config{Endpoint: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New (bare): %v", err)
	}
	defer bare.Close()
	if got := bare.Capabilities(); !got.Has(agents.CapHealthcheck) || !got.Has(agents.CapStats) {
		t.Errorf("bare Capabilities = %v, want CapHealthcheck|CapStats", got)
	}
	if got := bare.Capabilities(); got.Has(agents.CapProvision) || got.Has(agents.CapConfigSync) {
		t.Errorf("bare Capabilities = %v, want no CapProvision/CapConfigSync without Inbound/SyncConfig", got)
	}

	wired, err := xray.New(xray.Config{
		Endpoint:   "127.0.0.1:0",
		ServiceID:  "svc-1",
		Renderer:   xray.XrayJSON{},
		SyncConfig: stubSyncConfig(agents.ConfigSnapshot{Changed: true, Template: "{}"}, nil),
	})
	if err != nil {
		t.Fatalf("New (wired): %v", err)
	}
	defer wired.Close()
	if got := wired.Capabilities(); !got.Has(agents.CapConfigSync) {
		t.Errorf("wired Capabilities = %v, want CapConfigSync set", got)
	}
}

func TestXray_SyncConfigUnsupportedWithoutCallback(t *testing.T) {
	t.Parallel()
	c, err := xray.New(xray.Config{Endpoint: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	if _, err := c.SyncConfig(context.Background()); !errors.Is(err, agents.ErrUnsupported) {
		t.Fatalf("SyncConfig without callback: got %v, want ErrUnsupported", err)
	}
}

// End-to-end: snapshot from HUE → text/template render → ApplyConfig.
// Asserts that user UUIDs come from snap.Users (not vars), that vars
// substitute, and that the etag is cached.
func TestXray_SyncConfigRendersUsersAndCachesEtag(t *testing.T) {
	t.Parallel()
	snap := agents.ConfigSnapshot{
		Template: `{
  "inbounds": [{
    "port": {{.Vars.port}},
    "protocol": "vless",
    "settings": {
      "decryption": "none",
      "clients": [
        {{- range $i, $u := .Users -}}
        {{- if $i}},{{end}}
        { "id": {{$u.ID | quote}} }
        {{- end -}}
      ]
    },
    "streamSettings": {
      "network": "xhttp",
      "xhttpSettings": { "path": {{getVar "xray.xhttp.path" "/api" | quote}} }
    }
  }]
}`,
		TemplateFormat: "xray-json",
		Vars: map[string]string{
			"port":            "443",
			"xray.xhttp.path": "/explicit",
		},
		Users: []agents.ConfigUser{
			{ID: "11111111-2222-3333-4444-555555555555", Username: "alice"},
			{ID: "22222222-3333-4444-5555-666666666666", Username: "bob"},
		},
		Etag:    "etag-1",
		Changed: true,
	}

	var applied atomic.Int32
	var rendered []byte
	c, err := xray.New(xray.Config{
		Endpoint:   "127.0.0.1:0",
		ServiceID:  "svc-1",
		Renderer:   xray.XrayJSON{},
		SyncConfig: stubSyncConfig(snap, nil),
		ApplyConfig: func(b []byte) error {
			applied.Add(1)
			rendered = b
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	changed, err := c.SyncConfig(context.Background())
	if err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	if !changed {
		t.Fatal("first SyncConfig: changed=false, expected true")
	}
	if applied.Load() != 1 {
		t.Fatalf("ApplyConfig calls = %d, want 1", applied.Load())
	}
	if got := c.CachedEtag(); got != "etag-1" {
		t.Errorf("CachedEtag = %q, want %q", got, "etag-1")
	}

	got := string(rendered)
	for _, want := range []string{
		`"port": 443`,
		`"path": "/explicit"`,
		`"id": "11111111-2222-3333-4444-555555555555"`,
		`"id": "22222222-3333-4444-5555-666666666666"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q.\n--- output ---\n%s", want, got)
		}
	}
}

// Dotted-key fallback via getVar — author wrote a key that isn't in
// vars, default kicks in.
func TestXray_GetVarFallsBackToDefault(t *testing.T) {
	t.Parallel()
	snap := agents.ConfigSnapshot{
		Template: `{"path": {{getVar "missing.key" "/fallback" | quote}}}`,
		Vars:     map[string]string{},
		Changed:  true,
		Etag:     "e",
	}
	c, err := xray.New(xray.Config{
		Endpoint:    "127.0.0.1:0",
		ServiceID:   "svc-1",
		Renderer:    xray.XrayJSON{},
		SyncConfig:  stubSyncConfig(snap, nil),
		ApplyConfig: func([]byte) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	if _, err := c.SyncConfig(context.Background()); err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	if !strings.Contains(string(c.CachedConfig()), `"path": "/fallback"`) {
		t.Errorf("default did not apply.\n%s", c.CachedConfig())
	}
}

// Multiple-transport demo: one template emits two parallel inbounds
// (xhttp + ws) gated by a vars flag. Asserts both render from the
// same Users list.
func TestXray_MultipleTransportsFromOneTemplate(t *testing.T) {
	t.Parallel()
	tpl := `{
  "inbounds": [
    {
      "tag": "vless-xhttp",
      "port": {{.Vars.xhttp_port}},
      "protocol": "vless",
      "settings": { "decryption": "none", "clients": [
        {{- range $i, $u := .Users -}}
        {{- if $i}},{{end}}{ "id": {{$u.ID | quote}} }
        {{- end -}}
      ] },
      "streamSettings": { "network": "xhttp", "xhttpSettings": { "path": {{.Vars.xhttp_path | quote}} } }
    }
    {{- if eq (getVar "xray.ws.enabled" "false") "true" -}},
    {
      "tag": "vless-ws",
      "port": {{.Vars.ws_port}},
      "protocol": "vless",
      "settings": { "decryption": "none", "clients": [
        {{- range $i, $u := .Users -}}
        {{- if $i}},{{end}}{ "id": {{$u.ID | quote}} }
        {{- end -}}
      ] },
      "streamSettings": { "network": "ws", "wsSettings": { "path": {{.Vars.ws_path | quote}} } }
    }
    {{- end -}}
  ]
}`

	snap := agents.ConfigSnapshot{
		Template: tpl,
		Vars: map[string]string{
			"xhttp_port":      "443",
			"xhttp_path":      "/api",
			"ws_port":         "8443",
			"ws_path":         "/ws",
			"xray.ws.enabled": "true",
		},
		Users:   []agents.ConfigUser{{ID: "u1"}},
		Changed: true,
		Etag:    "e",
	}
	c, _ := xray.New(xray.Config{
		Endpoint:    "127.0.0.1:0",
		ServiceID:   "svc",
		Renderer:    xray.XrayJSON{},
		SyncConfig:  stubSyncConfig(snap, nil),
		ApplyConfig: func([]byte) error { return nil },
	})
	defer c.Close()
	if _, err := c.SyncConfig(context.Background()); err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	got := string(c.CachedConfig())
	for _, want := range []string{`"vless-xhttp"`, `"vless-ws"`, `"path": "/api"`, `"path": "/ws"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q.\n--- output ---\n%s", want, got)
		}
	}
}

func TestXray_SyncConfigShortCircuitsOnUnchangedEtag(t *testing.T) {
	t.Parallel()
	var applied atomic.Int32
	c, _ := xray.New(xray.Config{
		Endpoint:   "127.0.0.1:0",
		ServiceID:  "svc-1",
		Renderer:   xray.XrayJSON{},
		SyncConfig: stubSyncConfig(agents.ConfigSnapshot{Etag: "same", Changed: false}, nil),
		ApplyConfig: func([]byte) error {
			applied.Add(1)
			return nil
		},
	})
	defer c.Close()

	changed, err := c.SyncConfig(context.Background())
	if err != nil {
		t.Fatalf("SyncConfig: %v", err)
	}
	if changed {
		t.Error("changed=true, expected false on unchanged etag")
	}
	if applied.Load() != 0 {
		t.Errorf("ApplyConfig must not be called when unchanged; got %d calls", applied.Load())
	}
}

// Renderer must reject a template format it doesn't accept.
func TestXray_RendererFormatNegotiation(t *testing.T) {
	t.Parallel()
	c, _ := xray.New(xray.Config{
		Endpoint:    "127.0.0.1:0",
		ServiceID:   "svc-1",
		Renderer:    xray.XrayJSON{},
		SyncConfig:  stubSyncConfig(agents.ConfigSnapshot{Template: "x", TemplateFormat: "wireguard-ini", Changed: true, Etag: "e"}, nil),
		ApplyConfig: func([]byte) error { return nil },
	})
	defer c.Close()
	if _, err := c.SyncConfig(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "wireguard-ini") {
		t.Fatalf("expected format-mismatch error, got %v", err)
	}
}

// Bad templates fail loudly with the rendered text in the error so the
// operator can see what happened.
func TestXray_InvalidJSONInRenderedTemplate(t *testing.T) {
	t.Parallel()
	c, _ := xray.New(xray.Config{
		Endpoint:    "127.0.0.1:0",
		ServiceID:   "svc-1",
		Renderer:    xray.XrayJSON{},
		SyncConfig:  stubSyncConfig(agents.ConfigSnapshot{Template: `{"a": 1`, Changed: true, Etag: "e"}, nil),
		ApplyConfig: func([]byte) error { return nil },
	})
	defer c.Close()
	if _, err := c.SyncConfig(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("expected JSON-validation error, got %v", err)
	}
}

// Missing template var with missingkey=error must fail at render time
// rather than producing "<no value>" silently in the output.
func TestXray_MissingVarFailsLoudly(t *testing.T) {
	t.Parallel()
	c, _ := xray.New(xray.Config{
		Endpoint:    "127.0.0.1:0",
		ServiceID:   "svc-1",
		Renderer:    xray.XrayJSON{},
		SyncConfig:  stubSyncConfig(agents.ConfigSnapshot{Template: `{"port": {{.Vars.nope}}}`, Vars: map[string]string{}, Changed: true, Etag: "e"}, nil),
		ApplyConfig: func([]byte) error { return nil },
	})
	defer c.Close()
	if _, err := c.SyncConfig(context.Background()); err == nil {
		t.Fatal("expected missingkey error for unknown var")
	}
}

func TestXray_CapabilitiesIncludeProvisionWhenInboundSet(t *testing.T) {
	t.Parallel()
	c, err := xray.New(xray.Config{Endpoint: "127.0.0.1:0", Inbound: "vless-in"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	got := c.Capabilities()
	if !got.Has(agents.CapProvision) {
		t.Errorf("Capabilities = %v, want CapProvision set when Inbound is non-empty", got)
	}
	if !got.Has(agents.CapDisconnect) {
		t.Errorf("Capabilities = %v, want CapDisconnect set when Inbound is non-empty", got)
	}
}

func TestXray_AddRemoveUser_CallsHandlerService(t *testing.T) {
	t.Parallel()

	// Boot an in-process fake HandlerService.
	fake := &fakeHandlerService{}
	srv := grpc.NewServer()
	xraycmd.RegisterHandlerServiceServer(srv, fake)
	lis := bufconn.Listen(1 << 16)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	c, err := xray.New(xray.Config{
		Endpoint: "passthrough://buf",
		Inbound:  "vless-in",
	}, xray.WithDialOption(dialOpts...))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	u := agents.User{ID: "11111111-2222-3333-4444-555555555555", Tag: "alice@test"}

	if err := c.AddUser(ctx, u, ""); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if fake.addCalls.Load() != 1 {
		t.Errorf("AddUser: AlterInbound calls = %d, want 1", fake.addCalls.Load())
	}
	if got := fake.lastTag.Load(); got != "vless-in" {
		t.Errorf("AddUser: inbound tag = %q, want %q", got, "vless-in")
	}

	if err := c.RemoveUser(ctx, u); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}
	if fake.removeCalls.Load() != 1 {
		t.Errorf("RemoveUser: AlterInbound calls = %d, want 1", fake.removeCalls.Load())
	}
}

func TestXray_AddUserUnsupportedWithoutInbound(t *testing.T) {
	t.Parallel()
	c, err := xray.New(xray.Config{Endpoint: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	if err := c.AddUser(context.Background(), agents.User{}, ""); !errors.Is(err, agents.ErrUnsupported) {
		t.Fatalf("AddUser without Inbound: got %v, want ErrUnsupported", err)
	}
}

// ----- helpers -----

func stubSyncConfig(snap agents.ConfigSnapshot, err error) agents.SyncConfigFunc {
	return func(_ context.Context, _ string) (agents.ConfigSnapshot, error) {
		return snap, err
	}
}

func TestXray_ReadStats_ParsesCounters(t *testing.T) {
	fake := &fakeStatsService{stats: []*xraystats.Stat{
		{Name: "user>>>alice@test>>>traffic>>>uplink", Value: 1024},
		{Name: "user>>>alice@test>>>traffic>>>downlink", Value: 4096},
		{Name: "user>>>bob@test>>>traffic>>>uplink", Value: 512},
		{Name: "user>>>bob@test>>>traffic>>>downlink", Value: 0}, // zero — skipped
		{Name: "inbound>>>ignored>>>traffic>>>uplink", Value: 99}, // non-user — skipped
	}}
	srv := grpc.NewServer()
	xraystats.RegisterStatsServiceServer(srv, fake)
	lis := bufconn.Listen(1 << 16)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	c, err := xray.New(xray.Config{Endpoint: "passthrough://buf"},
		xray.WithDialOption(
			grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	deltas, err := c.ReadStats(context.Background())
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	byTag := make(map[string]agents.UsageDelta)
	for _, d := range deltas {
		byTag[d.User.Tag] = d
	}
	if got := byTag["alice@test"]; got.Upload != 1024 || got.Download != 4096 {
		t.Errorf("alice: up=%d down=%d, want 1024/4096", got.Upload, got.Download)
	}
	if got := byTag["bob@test"]; got.Upload != 512 || got.Download != 0 {
		t.Errorf("bob: up=%d down=%d, want 512/0", got.Upload, got.Download)
	}
	if _, ok := byTag[""]; ok {
		t.Error("non-user stat should be filtered out")
	}
	if fake.resetCalled.Load() != 1 {
		t.Errorf("QueryStats reset calls = %d, want 1", fake.resetCalled.Load())
	}
}

// fakeHandlerService records AlterInbound calls and classifies them by
// operation type for assertion.
type fakeHandlerService struct {
	xraycmd.UnimplementedHandlerServiceServer
	addCalls    atomic.Int32
	removeCalls atomic.Int32
	lastTag     atomic.Value // stores string
}

func (f *fakeHandlerService) AlterInbound(_ context.Context, req *xraycmd.AlterInboundRequest) (*emptypb.Empty, error) {
	f.lastTag.Store(req.GetTag())
	typeURL := req.GetOperation().GetTypeUrl()
	if strings.Contains(typeURL, "AddUserOperation") {
		f.addCalls.Add(1)
	} else if strings.Contains(typeURL, "RemoveUserOperation") {
		f.removeCalls.Add(1)
	}
	return &emptypb.Empty{}, nil
}

type fakeStatsService struct {
	xraystats.UnimplementedStatsServiceServer
	stats       []*xraystats.Stat
	resetCalled atomic.Int32
}

func (f *fakeStatsService) QueryStats(_ context.Context, req *xraystats.QueryStatsRequest) (*xraystats.QueryStatsResponse, error) {
	if req.GetReset_() {
		f.resetCalled.Add(1)
	}
	return &xraystats.QueryStatsResponse{Stat: f.stats}, nil
}
