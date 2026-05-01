package service

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// PenaltyTracker holds short-lived disconnect penalties applied when a user
// exceeds their concurrent-session limit. Penalties live in memory only —
// the legacy "logged in-memory but not permanent in DB" promise is
// preserved (PRD §3.2).
type PenaltyTracker struct {
	duration time.Duration

	mu        sync.RWMutex
	penalties map[uuid.UUID]time.Time // userID → expiresAt
}

func NewPenaltyTracker(duration time.Duration) *PenaltyTracker {
	return &PenaltyTracker{
		duration:  duration,
		penalties: make(map[uuid.UUID]time.Time),
	}
}

// Apply puts userID in penalty for the configured duration. If already
// penalized, the existing expiry is kept (no extension on repeated
// triggers — the previous code accidentally extended on every breach).
func (pt *PenaltyTracker) Apply(userID uuid.UUID, now time.Time) time.Time {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if existing, ok := pt.penalties[userID]; ok && existing.After(now) {
		return existing
	}
	expires := now.Add(pt.duration)
	pt.penalties[userID] = expires
	return expires
}

// Active reports whether the user is currently penalized; the second
// return is the expiration time when active.
func (pt *PenaltyTracker) Active(userID uuid.UUID, now time.Time) (bool, time.Time) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	expires, ok := pt.penalties[userID]
	if !ok || !expires.After(now) {
		return false, time.Time{}
	}
	return true, expires
}

// Reap drops penalties whose expiry is at or before now. Returns the
// number reaped — exposed for metrics/tests.
func (pt *PenaltyTracker) Reap(now time.Time) int {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	n := 0
	for u, exp := range pt.penalties {
		if !exp.After(now) {
			delete(pt.penalties, u)
			n++
		}
	}
	return n
}

// Forget unconditionally drops the penalty for a user (e.g., on delete or
// admin override).
func (pt *PenaltyTracker) Forget(userID uuid.UUID) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	delete(pt.penalties, userID)
}
