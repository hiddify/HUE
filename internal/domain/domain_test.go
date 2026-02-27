package domain

import (
	"testing"
	"time"
)

func TestUserAndPackageStateMethods(t *testing.T) {
	pkgID := "pkg-1"
	u := &User{Status: UserStatusActive, ActivePackageID: &pkgID}
	if !u.CanConnect() {
		t.Fatalf("expected active user with package to connect")
	}

	expires := time.Now().Add(2 * time.Hour)
	p := &Package{
		Status:       PackageStatusActive,
		TotalTraffic: 100,
		CurrentTotal: 20,
		ExpiresAt:    &expires,
	}
	if !p.CanUse() {
		t.Fatalf("expected active package with quota and no expiry to be usable")
	}

	p.CurrentTotal = 100
	if p.HasTrafficRemaining() {
		t.Fatalf("expected no remaining traffic at full usage")
	}
}

func TestPackageResetAndUsageAccounting(t *testing.T) {
	p := &Package{ResetMode: ResetModeDaily}
	next := p.CalculateNextReset()
	if next == nil {
		t.Fatalf("expected non-nil next reset for daily mode")
	}

	p.AddUsage(10, 20)
	if p.CurrentUpload != 10 || p.CurrentDownload != 20 || p.CurrentTotal != 30 {
		t.Fatalf("unexpected usage totals: up=%d down=%d total=%d", p.CurrentUpload, p.CurrentDownload, p.CurrentTotal)
	}
}

func TestNodeServiceAndTimeHelpers(t *testing.T) {
	n := &Node{TrafficMultiplier: 1.5}
	up, down := n.ApplyMultiplier(10, 20)
	if up != 15 || down != 30 {
		t.Fatalf("unexpected multiplier result: up=%d down=%d", up, down)
	}

	s := &Service{AllowedAuthMethods: []AuthMethod{AuthMethodUUID, AuthMethodPassword}}
	if !s.SupportsAuthMethod(AuthMethodUUID) || s.SupportsAuthMethod(AuthMethodPubKey) {
		t.Fatalf("unexpected auth method support result")
	}

	now := time.Now().Truncate(time.Second)
	unix := FormatTime(now)
	if unix == 0 {
		t.Fatalf("expected unix timestamp for non-zero time")
	}
	if ParseTime(unix).Unix() != now.Unix() {
		t.Fatalf("parse/format time mismatch")
	}
}
