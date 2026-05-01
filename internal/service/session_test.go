package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSessionTracker_HashIPIsDeterministic(t *testing.T) {
	t.Parallel()
	st := NewSessionTracker(time.Minute)
	a := st.HashIP("203.0.113.1")
	b := st.HashIP("203.0.113.1")
	if a != b {
		t.Fatalf("salt-stable hashes must be deterministic within a process")
	}
	c := st.HashIP("203.0.113.2")
	if a == c {
		t.Fatalf("different IPs must hash differently")
	}
	if st.HashIP("") != "" {
		t.Fatalf("empty IP must hash to empty string")
	}
}

func TestSessionTracker_DifferentTrackersHaveDifferentSalts(t *testing.T) {
	t.Parallel()
	a := NewSessionTracker(time.Minute).HashIP("203.0.113.1")
	b := NewSessionTracker(time.Minute).HashIP("203.0.113.1")
	if a == b {
		t.Fatalf("each SessionTracker must use its own random salt; got identical hashes across instances")
	}
}

func TestSessionTracker_CountWithinWindow(t *testing.T) {
	t.Parallel()
	st := NewSessionTracker(time.Minute)
	uid := uuid.New()
	now := time.Unix(1_700_000_000, 0)

	if got := st.Touch(uid, st.HashIP("1.1.1.1"), now); got != 1 {
		t.Fatalf("first touch: got %d, want 1", got)
	}
	if got := st.Touch(uid, st.HashIP("2.2.2.2"), now.Add(time.Second)); got != 2 {
		t.Fatalf("second touch (different IP): got %d, want 2", got)
	}
	if got := st.Touch(uid, st.HashIP("1.1.1.1"), now.Add(2*time.Second)); got != 2 {
		t.Fatalf("re-touch same IP must not increase count: got %d, want 2", got)
	}
}

func TestSessionTracker_WindowExpiry(t *testing.T) {
	t.Parallel()
	st := NewSessionTracker(time.Minute)
	uid := uuid.New()
	now := time.Unix(1_700_000_000, 0)

	st.Touch(uid, st.HashIP("1.1.1.1"), now)
	st.Touch(uid, st.HashIP("2.2.2.2"), now)

	// Both entries are 70s old now; both should age out.
	if got := st.Count(uid, now.Add(70*time.Second)); got != 0 {
		t.Fatalf("entries older than window must be swept: got %d, want 0", got)
	}
}

func TestSessionTracker_ForgetDropsAll(t *testing.T) {
	t.Parallel()
	st := NewSessionTracker(time.Minute)
	uid := uuid.New()
	now := time.Now()
	st.Touch(uid, st.HashIP("1.1.1.1"), now)
	st.Forget(uid)
	if got := st.Count(uid, now); got != 0 {
		t.Fatalf("Forget must clear all entries: got %d", got)
	}
}
