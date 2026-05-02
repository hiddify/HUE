package xray_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hiddify/hue/pkg/clients"
	"github.com/hiddify/hue/pkg/clients/xray"
)

// Compile-time assertion: the xray adapter implements clients.Client.
var _ clients.Client = (*xray.Client)(nil)

func TestXray_NewRejectsEmptyEndpoint(t *testing.T) {
	t.Parallel()
	if _, err := xray.New(xray.Config{}); err == nil {
		t.Error("New must reject an empty Endpoint")
	}
}

func TestXray_NewRequiresGeneratorWhenSyncConfigSet(t *testing.T) {
	t.Parallel()
	_, err := xray.New(xray.Config{
		Endpoint:   "127.0.0.1:0",
		ServiceID:  "svc-1",
		SyncConfig: stubSyncConfig(nil, "etag", true, nil),
		// no Generator
	})
	if err == nil || !strings.Contains(err.Error(), "Generator") {
		t.Fatalf("expected error about missing Generator, got %v", err)
	}
}

// Default capabilities advertise Healthcheck only when SyncConfig is
// not wired; CapConfigSync flips on once a callback is supplied.
func TestXray_CapabilitiesReflectWiring(t *testing.T) {
	t.Parallel()

	bare, err := xray.New(xray.Config{Endpoint: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New (bare): %v", err)
	}
	defer bare.Close()
	if got := bare.Capabilities(); got != clients.CapHealthcheck {
		t.Errorf("bare Capabilities = %v, want CapHealthcheck only", got)
	}

	wired, err := xray.New(xray.Config{
		Endpoint:   "127.0.0.1:0",
		ServiceID:  "svc-1",
		Generator:  xray.VlessXHTTP{},
		SyncConfig: stubSyncConfig(map[string]string{"port": "443"}, "e1", true, nil),
	})
	if err != nil {
		t.Fatalf("New (wired): %v", err)
	}
	defer wired.Close()
	if got := wired.Capabilities(); !got.Has(clients.CapConfigSync) {
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
	if _, err := c.SyncConfig(context.Background()); !errors.Is(err, clients.ErrUnsupported) {
		t.Fatalf("SyncConfig without callback: got %v, want ErrUnsupported", err)
	}
}

func TestXray_SyncConfigAppliesAndCachesEtag(t *testing.T) {
	t.Parallel()
	kv := map[string]string{
		"port":       "443",
		"user_uuid":  "11111111-2222-3333-4444-555555555555",
		"xhttp_path": "/api",
	}

	var applied atomic.Int32
	var lastBytes []byte

	c, err := xray.New(xray.Config{
		Endpoint:   "127.0.0.1:0",
		ServiceID:  "svc-1",
		Generator:  xray.VlessXHTTP{},
		SyncConfig: stubSyncConfig(kv, "etag-1", true, nil),
		ApplyConfig: func(b []byte) error {
			applied.Add(1)
			lastBytes = b
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
	if !strings.Contains(string(lastBytes), `"port":     443`) {
		t.Errorf("generated config does not contain the rendered port:\n%s", lastBytes)
	}
	if !strings.Contains(string(lastBytes), kv["user_uuid"]) {
		t.Error("generated config does not contain the user UUID")
	}
}

// When the server returns changed=false, the adapter must NOT regenerate
// or call ApplyConfig — that's the whole point of the etag short-circuit.
func TestXray_SyncConfigShortCircuitsOnUnchangedEtag(t *testing.T) {
	t.Parallel()
	var applied atomic.Int32
	c, err := xray.New(xray.Config{
		Endpoint:   "127.0.0.1:0",
		ServiceID:  "svc-1",
		Generator:  xray.VlessXHTTP{},
		SyncConfig: stubSyncConfig(nil, "same-etag", false, nil),
		ApplyConfig: func([]byte) error {
			applied.Add(1)
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
	if changed {
		t.Error("changed=true, expected false on unchanged etag")
	}
	if applied.Load() != 0 {
		t.Errorf("ApplyConfig must not be called when unchanged; got %d calls", applied.Load())
	}
}

func TestXray_SyncConfigPropagatesGeneratorErrors(t *testing.T) {
	t.Parallel()
	c, err := xray.New(xray.Config{
		Endpoint:    "127.0.0.1:0",
		ServiceID:   "svc-1",
		Generator:   xray.VlessXHTTP{},
		SyncConfig:  stubSyncConfig(map[string]string{"port": "443"}, "e", true, nil), // missing user_uuid
		ApplyConfig: func([]byte) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	if _, err := c.SyncConfig(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "user_uuid") {
		t.Fatalf("expected generator error about user_uuid, got %v", err)
	}
}

// ----- VlessXHTTP unit tests -----

func TestVlessXHTTP_RejectsMissingRequiredKeys(t *testing.T) {
	t.Parallel()
	cases := []map[string]string{
		nil,
		{},
		{"port": "443"},
		{"port": "443", "user_uuid": "u"},
	}
	for i, kv := range cases {
		if _, err := (xray.VlessXHTTP{}).Generate(kv); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

func TestVlessXHTTP_TLSRequiresBothCertAndKey(t *testing.T) {
	t.Parallel()
	kv := map[string]string{
		"port":         "443",
		"user_uuid":    "u",
		"xhttp_path":   "/p",
		"tls_cert_pem": "CERT",
		// missing tls_key_pem
	}
	if _, err := (xray.VlessXHTTP{}).Generate(kv); err == nil ||
		!strings.Contains(err.Error(), "tls_key_pem") {
		t.Fatalf("expected tls_key_pem error, got %v", err)
	}
}

func TestRegistry_LookupBuiltin(t *testing.T) {
	t.Parallel()
	if g := xray.Generator("vless-xhttp"); g == nil {
		t.Fatal("vless-xhttp generator must be pre-registered")
	}
	if g := xray.Generator("nonexistent"); g != nil {
		t.Errorf("Generator(unknown) = %v, want nil", g)
	}
}

// ----- helpers -----

func stubSyncConfig(kv map[string]string, etag string, changed bool, err error) clients.SyncConfigFunc {
	return func(_ context.Context, _ string) (map[string]string, string, bool, error) {
		return kv, etag, changed, err
	}
}
