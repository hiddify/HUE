package auth

import "testing"

func TestLockManagerReturnsStableLocks(t *testing.T) {
	lm := NewLockManager()

	userLock1 := lm.GetUserLock("u1")
	userLock2 := lm.GetUserLock("u1")
	if userLock1 != userLock2 {
		t.Fatalf("expected same user lock instance for same key")
	}

	nodeLock1 := lm.GetNodeLock("n1")
	nodeLock2 := lm.GetNodeLock("n1")
	if nodeLock1 != nodeLock2 {
		t.Fatalf("expected same node lock instance for same key")
	}

	svcLock1 := lm.GetServiceLock("s1")
	svcLock2 := lm.GetServiceLock("s1")
	if svcLock1 != svcLock2 {
		t.Fatalf("expected same service lock instance for same key")
	}
}

func TestScopedLocksRelease(t *testing.T) {
	lm := NewLockManager()

	r := lm.NewScopedReadLock("u1")
	r.Release()

	w := lm.NewScopedWriteLock("u1")
	w.Release()
}
