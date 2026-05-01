package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPenaltyTracker_AppliesAndExpires(t *testing.T) {
	t.Parallel()
	pt := NewPenaltyTracker(10 * time.Minute)
	uid := uuid.New()
	now := time.Unix(1_700_000_000, 0)

	if active, _ := pt.Active(uid, now); active {
		t.Fatal("user starts not penalized")
	}
	until := pt.Apply(uid, now)
	if want := now.Add(10 * time.Minute); !until.Equal(want) {
		t.Fatalf("apply: got %v, want %v", until, want)
	}
	if active, _ := pt.Active(uid, now.Add(time.Minute)); !active {
		t.Fatal("user must be active during penalty window")
	}
	if active, _ := pt.Active(uid, until); active {
		t.Fatal("user must not be active at exact expiry")
	}
}

func TestPenaltyTracker_DoesNotExtendOnRetrigger(t *testing.T) {
	t.Parallel()
	// Regression test: legacy code accidentally extended penalties on
	// every breach, which let abusive users get permanently penalized
	// once they hit the limit twice in a row.
	pt := NewPenaltyTracker(10 * time.Minute)
	uid := uuid.New()
	t0 := time.Unix(1_700_000_000, 0)

	first := pt.Apply(uid, t0)
	second := pt.Apply(uid, t0.Add(time.Minute)) // retrigger inside the window

	if !first.Equal(second) {
		t.Fatalf("retrigger inside the window must NOT extend the penalty; first=%v second=%v", first, second)
	}
}

func TestPenaltyTracker_ReapAndForget(t *testing.T) {
	t.Parallel()
	pt := NewPenaltyTracker(time.Second)
	for range 5 {
		pt.Apply(uuid.New(), time.Now())
	}
	uid := uuid.New()
	pt.Apply(uid, time.Now())
	pt.Forget(uid)

	if active, _ := pt.Active(uid, time.Now()); active {
		t.Fatal("Forget must drop the penalty immediately")
	}

	// Reap after the duration drops the rest.
	got := pt.Reap(time.Now().Add(2 * time.Second))
	if got != 5 {
		t.Fatalf("Reap: got %d expired, want 5", got)
	}
}
