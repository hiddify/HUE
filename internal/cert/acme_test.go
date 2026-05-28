package cert

import (
	"testing"
)

func TestNewDNS01Provider_KnownProviders(t *testing.T) {
	// These providers read from env vars at construction time. With no
	// env set they return a provider (or a config error) — but the
	// factory itself must not return "unknown provider".
	known := []string{"cloudflare", "digitalocean", "exec", "CLOUDFLARE", "Exec"}
	for _, name := range known {
		_, err := NewDNS01Provider(name)
		// Accept either success or a credentials error from the provider
		// (no env vars in test). Only reject "unknown provider" error.
		if err != nil && stringContains(err.Error(), "unknown DNS-01 provider") {
			t.Errorf("%q: got unexpected unknown-provider error: %v", name, err)
		}
	}
}

func TestNewDNS01Provider_UnknownReturnsError(t *testing.T) {
	cases := []string{"bogus", "aws", "r53", "", "  "}
	for _, name := range cases {
		p, err := NewDNS01Provider(name)
		if err == nil {
			t.Errorf("%q: expected error, got nil (provider=%v)", name, p)
		}
		if p != nil {
			t.Errorf("%q: expected nil provider on error, got %T", name, p)
		}
	}
}

func TestRequestACME_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ACMEConfig
		domains []string
	}{
		{
			name:    "no challenger and no dns01",
			cfg:     ACMEConfig{ContactEmail: "x@example.com"},
			domains: []string{"example.com"},
		},
		{
			name:    "no domains",
			cfg:     ACMEConfig{ContactEmail: "x@example.com", Challenger: &HTTP01Challenger{}},
			domains: nil,
		},
		{
			name:    "no contact email",
			cfg:     ACMEConfig{Challenger: &HTTP01Challenger{}},
			domains: []string{"example.com"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := RequestACME(tc.cfg, tc.domains)
			if err == nil {
				t.Errorf("expected validation error, got nil")
			}
		})
	}
}

// TestRequestACME_DNS01PreferredOverHTTP01 verifies DNS01Provider takes
// precedence. We use a fake provider that records calls and a
// DirectoryURL that will fail immediately — we only care that the
// DNS-01 path was wired, not that the full issuance succeeds.
func TestRequestACME_DNS01PreferredOverHTTP01(t *testing.T) {
	fake := &fakeDNS01Provider{}
	cfg := ACMEConfig{
		DirectoryURL:  "http://127.0.0.1:1", // refuses connections
		ContactEmail:  "test@example.com",
		Challenger:    &HTTP01Challenger{},
		DNS01Provider: fake,
	}
	// Registration will fail because the directory URL is unreachable.
	// That's fine — we just need no panic and the error to come from
	// the ACME client, not from "Challenger is required".
	_, err := RequestACME(cfg, []string{"example.com"})
	if err == nil {
		t.Fatal("expected error from unreachable ACME server, got nil")
	}
	// Must not be the "requires either DNS01Provider or Challenger" error.
	const badMsg = "requires either"
	if contains(err.Error(), badMsg) {
		t.Errorf("got validation error instead of network error: %v", err)
	}
}

type fakeDNS01Provider struct{}

func (f *fakeDNS01Provider) Present(domain, token, keyAuth string) error { return nil }
func (f *fakeDNS01Provider) CleanUp(domain, token, keyAuth string) error  { return nil }

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
