package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SessionTracker counts the number of distinct client IPs a user has been
// seen from inside a sliding time window — used to enforce
// `max_concurrent` on a usage plan.
//
// The salt used to hash the IPs is generated once at process start and held
// in memory. The previous implementation rotated salt by calendar day,
// which both (a) allowed correlation across the whole day and (b) caused a
// discontinuity in session counts at midnight (REVIEW.md L2). A
// process-lifetime random salt is sufficient since sessions are short-lived
// relative to process lifetime.
type SessionTracker struct {
	salt   []byte
	window time.Duration

	mu       sync.Mutex
	sessions map[uuid.UUID]map[string]time.Time // userID → ipHash → lastSeen
}

func NewSessionTracker(window time.Duration) *SessionTracker {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		// rand.Read on Linux/Darwin uses getrandom(2); failure here means
		// the OS is wedged. Panic is the right move.
		panic("session tracker: cannot read entropy: " + err.Error())
	}
	return &SessionTracker{
		salt:     salt,
		window:   window,
		sessions: make(map[uuid.UUID]map[string]time.Time),
	}
}

// HashIP returns a salted SHA-256 hash of the IP. Caller must drop the
// original IP string immediately after this returns to honor the
// zero-IP-retention policy.
func (st *SessionTracker) HashIP(ipStr string) string {
	if ipStr == "" {
		return ""
	}
	h := sha256.New()
	h.Write(st.salt)
	h.Write([]byte(ipStr))
	return hex.EncodeToString(h.Sum(nil))
}

// Touch records that user was seen from ipHash at now and returns the
// current count of distinct IPs in the window.
func (st *SessionTracker) Touch(userID uuid.UUID, ipHash string, now time.Time) int {
	if ipHash == "" {
		return st.count(userID, now)
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	user, ok := st.sessions[userID]
	if !ok {
		user = make(map[string]time.Time)
		st.sessions[userID] = user
	}
	user[ipHash] = now
	st.sweepLocked(user, now)
	return len(user)
}

// Count returns the number of distinct IPs the user was seen from in the
// window without updating state.
func (st *SessionTracker) Count(userID uuid.UUID, now time.Time) int {
	return st.count(userID, now)
}

func (st *SessionTracker) count(userID uuid.UUID, now time.Time) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	user, ok := st.sessions[userID]
	if !ok {
		return 0
	}
	st.sweepLocked(user, now)
	return len(user)
}

func (st *SessionTracker) sweepLocked(user map[string]time.Time, now time.Time) {
	cutoff := now.Add(-st.window)
	for hash, last := range user {
		if last.Before(cutoff) {
			delete(user, hash)
		}
	}
}

// Forget drops all sessions for a user (e.g., on user delete).
func (st *SessionTracker) Forget(userID uuid.UUID) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, userID)
}
