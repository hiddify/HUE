// Package service holds the business logic of the HUE engine — quota
// enforcement, concurrent-session counting, penalties, manager-hierarchy
// rollups, and the orchestration glue that ties them to the database.
//
// Service code never sees proto types: the gRPC layer translates inbound
// messages into service inputs and outputs back. This keeps the engine
// reusable from tests, benchmarks, and future transports.
package service

import (
	"sync"

	"github.com/google/uuid"
)

// LockManager hands out per-resource RWMutexes keyed by UUID, with explicit
// removal on resource delete to avoid the unbounded-growth bug that the
// legacy code had (see REVIEW.md M3).
//
// The pattern is intentionally simple: callers Acquire, defer the returned
// release function, and call Forget when the underlying resource (user,
// service, etc.) is deleted.
type LockManager struct {
	locks sync.Map // uuid.UUID → *sync.RWMutex
}

func NewLockManager() *LockManager { return &LockManager{} }

// Acquire takes a write lock on id. The returned function releases it.
func (lm *LockManager) Acquire(id uuid.UUID) func() {
	mu := lm.mutexFor(id)
	mu.Lock()
	return mu.Unlock
}

// AcquireRead takes a read lock on id. The returned function releases it.
func (lm *LockManager) AcquireRead(id uuid.UUID) func() {
	mu := lm.mutexFor(id)
	mu.RLock()
	return mu.RUnlock
}

// Forget removes the lock entry for id. Safe to call when the resource is
// deleted; if any goroutine still holds the mutex, it keeps a private
// reference and continues to work — only future LoadOrStore calls miss.
func (lm *LockManager) Forget(id uuid.UUID) {
	lm.locks.Delete(id)
}

// Size returns the current number of tracked locks. Test helper.
func (lm *LockManager) Size() int {
	n := 0
	lm.locks.Range(func(_, _ any) bool { n++; return true })
	return n
}

func (lm *LockManager) mutexFor(id uuid.UUID) *sync.RWMutex {
	if existing, ok := lm.locks.Load(id); ok {
		return existing.(*sync.RWMutex)
	}
	created := &sync.RWMutex{}
	actual, _ := lm.locks.LoadOrStore(id, created)
	return actual.(*sync.RWMutex)
}
