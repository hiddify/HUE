package cache

import (
	"sync"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
)

// MemoryCache provides in-memory caching for active users and sessions
type MemoryCache struct {
	// User status cache
	users sync.Map // map[string]*UserCacheEntry

	// Session tracking
	sessions sync.Map // map[string]*SessionCache // key: userID

	// Penalty tracking
	penalties sync.Map // map[string]*PenaltyEntry // key: userID

	// Node cache
	nodes sync.Map // map[string]*NodeCacheEntry

	// Prepared disconnect commands
	disconnectQueue []*DisconnectCommand
	disconnectMu    sync.Mutex
}

// UserCacheEntry represents cached user data
type UserCacheEntry struct {
	UserID          string
	Status          domain.UserStatus
	ActivePackageID *string
	CurrentUpload   int64
	CurrentDownload int64
	CurrentTotal    int64
	MaxConcurrent   int
	LastUpdated     time.Time
}

// SessionCache tracks active sessions for a user
type SessionCache struct {
	UserID   string
	Sessions map[string]*SessionEntry // key: IP hash or session ID
	mu       sync.RWMutex
}

// SessionEntry represents an active session
type SessionEntry struct {
	SessionID  string
	IPHash     string // Hashed IP for privacy
	Country    string
	City       string
	ISP        string
	StartedAt  time.Time
	LastSeenAt time.Time
}

// PenaltyEntry tracks a temporary penalty
type PenaltyEntry struct {
	UserID    string
	Reason    string
	AppliedAt time.Time
	ExpiresAt time.Time
}

// NodeCacheEntry represents cached node data
type NodeCacheEntry struct {
	NodeID            string
	TrafficMultiplier float64
	CurrentUpload     int64
	CurrentDownload   int64
	LastUpdated       time.Time
}

// DisconnectCommand represents a pending disconnect command
type DisconnectCommand struct {
	UserID    string
	SessionID string
	Reason    string
	NodeID    string
}

// NewMemoryCache creates a new MemoryCache instance
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		disconnectQueue: make([]*DisconnectCommand, 0, 100),
	}
}

// User operations

// SetUser caches user data
func (c *MemoryCache) SetUser(userID string, status domain.UserStatus, packageID *string, maxConcurrent int) {
	c.users.Store(userID, &UserCacheEntry{
		UserID:          userID,
		Status:          status,
		ActivePackageID: packageID,
		MaxConcurrent:   maxConcurrent,
		LastUpdated:     time.Now(),
	})
}

// GetUser retrieves cached user data
func (c *MemoryCache) GetUser(userID string) *UserCacheEntry {
	if v, ok := c.users.Load(userID); ok {
		return v.(*UserCacheEntry)
	}
	return nil
}

// UpdateUserUsage updates the cached usage counters
func (c *MemoryCache) UpdateUserUsage(userID string, upload, download int64) {
	if v, ok := c.users.Load(userID); ok {
		entry := v.(*UserCacheEntry)
		entry.CurrentUpload += upload
		entry.CurrentDownload += download
		entry.CurrentTotal += upload + download
		entry.LastUpdated = time.Now()
	}
}

// DeleteUser removes user from cache
func (c *MemoryCache) DeleteUser(userID string) {
	c.users.Delete(userID)
	c.sessions.Delete(userID)
	c.penalties.Delete(userID)
}

// Session operations

// GetOrCreateSessionCache gets or creates session cache for a user
func (c *MemoryCache) GetOrCreateSessionCache(userID string) *SessionCache {
	if v, ok := c.sessions.Load(userID); ok {
		return v.(*SessionCache)
	}

	sc := &SessionCache{
		UserID:   userID,
		Sessions: make(map[string]*SessionEntry),
	}
	actual, _ := c.sessions.LoadOrStore(userID, sc)
	return actual.(*SessionCache)
}

// AddSession adds a new session
func (sc *SessionCache) AddSession(sessionID, ipHash, country, city, isp string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	now := time.Now()
	sc.Sessions[sessionID] = &SessionEntry{
		SessionID:  sessionID,
		IPHash:     ipHash,
		Country:    country,
		City:       city,
		ISP:        isp,
		StartedAt:  now,
		LastSeenAt: now,
	}
}

// UpdateSessionLastSeen updates the last seen time for a session
func (sc *SessionCache) UpdateSessionLastSeen(sessionID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if session, ok := sc.Sessions[sessionID]; ok {
		session.LastSeenAt = time.Now()
	}
}

// RemoveSession removes a session
func (sc *SessionCache) RemoveSession(sessionID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	delete(sc.Sessions, sessionID)
}

// GetActiveSessionCount returns the number of active sessions within the window
func (sc *SessionCache) GetActiveSessionCount(window time.Duration) int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	now := time.Now()
	count := 0

	for _, session := range sc.Sessions {
		if now.Sub(session.LastSeenAt) <= window {
			count++
		}
	}

	return count
}

// HasSession checks if a session exists
func (sc *SessionCache) HasSession(sessionID string) bool {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	_, ok := sc.Sessions[sessionID]
	return ok
}

// GetSessions returns all sessions
func (sc *SessionCache) GetSessions() []*SessionEntry {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	sessions := make([]*SessionEntry, 0, len(sc.Sessions))
	for _, s := range sc.Sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

// Penalty operations

// SetPenalty sets a penalty for a user
func (c *MemoryCache) SetPenalty(userID, reason string, duration time.Duration) {
	c.penalties.Store(userID, &PenaltyEntry{
		UserID:    userID,
		Reason:    reason,
		AppliedAt: time.Now(),
		ExpiresAt: time.Now().Add(duration),
	})
}

// GetPenalty gets the current penalty for a user
func (c *MemoryCache) GetPenalty(userID string) *PenaltyEntry {
	if v, ok := c.penalties.Load(userID); ok {
		entry := v.(*PenaltyEntry)
		// Check if penalty has expired
		if time.Now().After(entry.ExpiresAt) {
			c.penalties.Delete(userID)
			return nil
		}
		return entry
	}
	return nil
}

// ClearPenalty removes a penalty
func (c *MemoryCache) ClearPenalty(userID string) {
	c.penalties.Delete(userID)
}

// RangePenalties iterates over all penalties
func (c *MemoryCache) RangePenalties(fn func(userID string, penalty *PenaltyEntry) bool) {
	c.penalties.Range(func(key, value interface{}) bool {
		return fn(key.(string), value.(*PenaltyEntry))
	})
}

// RangeSessions iterates over all sessions for a user
func (c *MemoryCache) RangeSessions(userID string, fn func(sessionID string, session *SessionEntry) bool) {
	if v, ok := c.sessions.Load(userID); ok {
		sc := v.(*SessionCache)
		sc.mu.RLock()
		defer sc.mu.RUnlock()
		for sid, s := range sc.Sessions {
			if !fn(sid, s) {
				break
			}
		}
	}
}

// RangeAllSessions iterates over all users' sessions
func (c *MemoryCache) RangeAllSessions(fn func(userID string, sessionCache *SessionCache) bool) {
	c.sessions.Range(func(key, value interface{}) bool {
		return fn(key.(string), value.(*SessionCache))
	})
}

// RemoveStaleSessions removes sessions older than the window
func (sc *SessionCache) RemoveStaleSessions(window time.Duration, count *int) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	now := time.Now()
	for sessionID, session := range sc.Sessions {
		if now.Sub(session.LastSeenAt) > window {
			delete(sc.Sessions, sessionID)
			*count++
		}
	}
}

// Node operations

// SetNode caches node data
func (c *MemoryCache) SetNode(nodeID string, multiplier float64) {
	c.nodes.Store(nodeID, &NodeCacheEntry{
		NodeID:            nodeID,
		TrafficMultiplier: multiplier,
		LastUpdated:       time.Now(),
	})
}

// GetNode retrieves cached node data
func (c *MemoryCache) GetNode(nodeID string) *NodeCacheEntry {
	if v, ok := c.nodes.Load(nodeID); ok {
		return v.(*NodeCacheEntry)
	}
	return nil
}

// UpdateNodeUsage updates cached node usage
func (c *MemoryCache) UpdateNodeUsage(nodeID string, upload, download int64) {
	if v, ok := c.nodes.Load(nodeID); ok {
		entry := v.(*NodeCacheEntry)
		entry.CurrentUpload += upload
		entry.CurrentDownload += download
		entry.LastUpdated = time.Now()
	}
}

// Disconnect queue operations

// QueueDisconnect adds a disconnect command to the queue
func (c *MemoryCache) QueueDisconnect(userID, sessionID, reason, nodeID string) {
	c.disconnectMu.Lock()
	defer c.disconnectMu.Unlock()

	c.disconnectQueue = append(c.disconnectQueue, &DisconnectCommand{
		UserID:    userID,
		SessionID: sessionID,
		Reason:    reason,
		NodeID:    nodeID,
	})
}

// GetDisconnectBatch retrieves and clears the disconnect queue
func (c *MemoryCache) GetDisconnectBatch() []*DisconnectCommand {
	c.disconnectMu.Lock()
	defer c.disconnectMu.Unlock()

	batch := c.disconnectQueue
	c.disconnectQueue = make([]*DisconnectCommand, 0, 100)
	return batch
}
