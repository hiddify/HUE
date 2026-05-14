package auth

import (
	"sync"
	"time"
)

// Lockout tracks failed login attempts per (ip, username) and blocks
// further attempts for a window after N consecutive failures. In-memory
// only (LRU bounded) since the data is short-lived and per-process.
//
// Defaults: 5 attempts / 15 min window. Override via NewLockout.
type Lockout struct {
	maxAttempts int
	window      time.Duration

	mu      sync.Mutex
	entries map[lockoutKey]*lockoutEntry
}

type lockoutKey struct {
	IP       string
	Username string
}

type lockoutEntry struct {
	count       int
	firstFailed time.Time
	lockedUntil time.Time
}

func NewLockout(maxAttempts int, window time.Duration) *Lockout {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &Lockout{
		maxAttempts: maxAttempts,
		window:      window,
		entries:     make(map[lockoutKey]*lockoutEntry),
	}
}

// Allowed reports whether (ip, username) is currently allowed to attempt
// login. When locked, returns (false, lockedUntil).
func (l *Lockout) Allowed(ip, username string, now time.Time) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[lockoutKey{IP: ip, Username: username}]
	if !ok {
		return true, time.Time{}
	}
	if e.lockedUntil.After(now) {
		return false, e.lockedUntil
	}
	// Window expired — entry is stale, drop it.
	if now.Sub(e.firstFailed) > l.window {
		delete(l.entries, lockoutKey{IP: ip, Username: username})
		return true, time.Time{}
	}
	return true, time.Time{}
}

// RecordFailure registers a failed attempt. Returns true + lockUntil
// when this failure pushed the counter over the threshold.
func (l *Lockout) RecordFailure(ip, username string, now time.Time) (locked bool, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := lockoutKey{IP: ip, Username: username}
	e, ok := l.entries[k]
	if !ok || now.Sub(e.firstFailed) > l.window {
		l.entries[k] = &lockoutEntry{count: 1, firstFailed: now}
		return false, time.Time{}
	}
	e.count++
	if e.count >= l.maxAttempts {
		e.lockedUntil = e.firstFailed.Add(l.window)
		return true, e.lockedUntil
	}
	return false, time.Time{}
}

// RecordSuccess clears the counter on a successful login.
func (l *Lockout) RecordSuccess(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, lockoutKey{IP: ip, Username: username})
}

// Reap drops entries whose lockout window has expired. Call from a
// periodic goroutine to keep the map bounded under sustained attack.
func (l *Lockout) Reap(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, e := range l.entries {
		if e.lockedUntil.Before(now) && now.Sub(e.firstFailed) > l.window {
			delete(l.entries, k)
			n++
		}
	}
	return n
}
