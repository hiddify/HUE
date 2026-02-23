package auth

import (
	"sync"
)

// LockManager provides fine-grained locking for users, nodes, and services
type LockManager struct {
	userLocks    sync.Map // map[string]*sync.RWMutex
	nodeLocks    sync.Map // map[string]*sync.RWMutex
	serviceLocks sync.Map // map[string]*sync.RWMutex
}

// NewLockManager creates a new LockManager instance
func NewLockManager() *LockManager {
	return &LockManager{}
}

// User Locks

// GetUserLock gets or creates a lock for a user
func (lm *LockManager) GetUserLock(userID string) *sync.RWMutex {
	if v, ok := lm.userLocks.Load(userID); ok {
		return v.(*sync.RWMutex)
	}

	lock := &sync.RWMutex{}
	actual, _ := lm.userLocks.LoadOrStore(userID, lock)
	return actual.(*sync.RWMutex)
}

// LockUser locks a user exclusively
func (lm *LockManager) LockUser(userID string) {
	lm.GetUserLock(userID).Lock()
}

// UnlockUser unlocks a user
func (lm *LockManager) UnlockUser(userID string) {
	lm.GetUserLock(userID).Unlock()
}

// RLockUser locks a user for reading
func (lm *LockManager) RLockUser(userID string) {
	lm.GetUserLock(userID).RLock()
}

// RUnlockUser unlocks a user for reading
func (lm *LockManager) RUnlockUser(userID string) {
	lm.GetUserLock(userID).RUnlock()
}

// Node Locks

// GetNodeLock gets or creates a lock for a node
func (lm *LockManager) GetNodeLock(nodeID string) *sync.RWMutex {
	if v, ok := lm.nodeLocks.Load(nodeID); ok {
		return v.(*sync.RWMutex)
	}

	lock := &sync.RWMutex{}
	actual, _ := lm.nodeLocks.LoadOrStore(nodeID, lock)
	return actual.(*sync.RWMutex)
}

// LockNode locks a node exclusively
func (lm *LockManager) LockNode(nodeID string) {
	lm.GetNodeLock(nodeID).Lock()
}

// UnlockNode unlocks a node
func (lm *LockManager) UnlockNode(nodeID string) {
	lm.GetNodeLock(nodeID).Unlock()
}

// RLockNode locks a node for reading
func (lm *LockManager) RLockNode(nodeID string) {
	lm.GetNodeLock(nodeID).RLock()
}

// RUnlockNode unlocks a node for reading
func (lm *LockManager) RUnlockNode(nodeID string) {
	lm.GetNodeLock(nodeID).RUnlock()
}

// Service Locks

// GetServiceLock gets or creates a lock for a service
func (lm *LockManager) GetServiceLock(serviceID string) *sync.RWMutex {
	if v, ok := lm.serviceLocks.Load(serviceID); ok {
		return v.(*sync.RWMutex)
	}

	lock := &sync.RWMutex{}
	actual, _ := lm.serviceLocks.LoadOrStore(serviceID, lock)
	return actual.(*sync.RWMutex)
}

// LockService locks a service exclusively
func (lm *LockManager) LockService(serviceID string) {
	lm.GetServiceLock(serviceID).Lock()
}

// UnlockService unlocks a service
func (lm *LockManager) UnlockService(serviceID string) {
	lm.GetServiceLock(serviceID).Unlock()
}

// RLockService locks a service for reading
func (lm *LockManager) RLockService(serviceID string) {
	lm.GetServiceLock(serviceID).RLock()
}

// RUnlockService unlocks a service for reading
func (lm *LockManager) RUnlockService(serviceID string) {
	lm.GetServiceLock(serviceID).RUnlock()
}

// ScopedLock provides RAII-style locking
type ScopedLock struct {
	lock   *sync.RWMutex
	write  bool
}

// NewScopedReadLock creates a scoped read lock
func (lm *LockManager) NewScopedReadLock(userID string) *ScopedLock {
	lock := lm.GetUserLock(userID)
	lock.RLock()
	return &ScopedLock{lock: lock, write: false}
}

// NewScopedWriteLock creates a scoped write lock
func (lm *LockManager) NewScopedWriteLock(userID string) *ScopedLock {
	lock := lm.GetUserLock(userID)
	lock.Lock()
	return &ScopedLock{lock: lock, write: true}
}

// Release releases the lock
func (sl *ScopedLock) Release() {
	if sl.write {
		sl.lock.Unlock()
	} else {
		sl.lock.RUnlock()
	}
}
