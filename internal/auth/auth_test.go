package auth

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc/peer"
)

func TestAuthenticatorValidateSecretAndIPAllowlist(t *testing.T) {
	a, err := NewAuthenticator("secret-1", "", "", []string{"10.0.0.0/8", "127.0.0.1"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	if !a.ValidateSecret("secret-1") {
		t.Fatalf("expected secret to validate")
	}
	if a.ValidateSecret("wrong") {
		t.Fatalf("expected wrong secret to fail")
	}

	if !a.IsIPAllowed("10.1.2.3") {
		t.Fatalf("expected CIDR-allowed IP to pass")
	}
	if !a.IsIPAllowed("127.0.0.1") {
		t.Fatalf("expected exact-allowed IP to pass")
	}
	if a.IsIPAllowed("192.168.1.10") {
		t.Fatalf("expected IP outside allowlist to fail")
	}
}

func TestAuthenticatorClientIPExtraction(t *testing.T) {
	a, err := NewAuthenticator("s", "", "", nil)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443}})
	if got := a.GetClientIP(ctx); got != "8.8.8.8" {
		t.Fatalf("expected 8.8.8.8, got %s", got)
	}
}

func TestAuthenticatorRejectsInvalidCIDR(t *testing.T) {
	if _, err := NewAuthenticator("s", "", "", []string{"not-an-ip"}); err == nil {
		t.Fatalf("expected invalid CIDR/IP to return error")
	}
}
