package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLockManager_PerIDIsolation(t *testing.T) {
	t.Parallel()
	lm := NewLockManager()
	a, b := uuid.New(), uuid.New()

	releaseA := lm.Acquire(a)
	defer releaseA()

	done := make(chan struct{})
	go func() {
		release := lm.Acquire(b)
		release()
		close(done)
	}()

	select {
	case <-done:
		// Different IDs must not block each other.
	case <-time.After(time.Second):
		t.Fatalf("Acquire(b) blocked while Acquire(a) was held — locks are not per-ID")
	}
}

func TestLockManager_SameIDSerializes(t *testing.T) {
	t.Parallel()
	lm := NewLockManager()
	id := uuid.New()

	const N = 100
	var counter atomic.Int64
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			release := lm.Acquire(id)
			defer release()
			// Non-atomic increment under the lock. If the lock isn't
			// serializing, the race detector will flag this.
			v := counter.Load()
			time.Sleep(time.Microsecond)
			counter.Store(v + 1)
		}()
	}
	wg.Wait()

	if got := counter.Load(); got != N {
		t.Fatalf("expected counter %d under serialized lock, got %d", N, got)
	}
}

func TestLockManager_ForgetCleansUp(t *testing.T) {
	t.Parallel()
	lm := NewLockManager()
	for range 1000 {
		id := uuid.New()
		release := lm.Acquire(id)
		release()
		lm.Forget(id)
	}
	if sz := lm.Size(); sz != 0 {
		t.Fatalf("expected lock map to be empty after Forget, got %d entries", sz)
	}
}

func TestLockManager_ChurnDoesNotLeak(t *testing.T) {
	t.Parallel()
	// Stress: 10k create+forget cycles. The legacy code leaked here
	// (REVIEW.md M3); this test would have caught it.
	lm := NewLockManager()
	for range 10_000 {
		id := uuid.New()
		release := lm.Acquire(id)
		release()
		lm.Forget(id)
	}
	if sz := lm.Size(); sz != 0 {
		t.Fatalf("leak: lock map size = %d after 10k cycles", sz)
	}
}
