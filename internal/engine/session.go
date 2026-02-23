package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/storage/cache"
	"go.uber.org/zap"
)

// SessionManager handles concurrent session tracking and enforcement
type SessionManager struct {
	cache  *cache.MemoryCache
	window time.Duration
	logger *zap.Logger
}

// NewSessionManager creates a new SessionManager instance
func NewSessionManager(cache *cache.MemoryCache, window time.Duration, logger *zap.Logger) *SessionManager {
	return &SessionManager{
		cache:  cache,
		window: window,
		logger: logger,
	}
}

// SessionResult represents the result of a session check
type SessionResult struct {
	UserID          string
	SessionID       string
	Allowed         bool
	CurrentCount    int
	MaxConcurrent   int
	SessionLimitHit bool
	Reason          string
	IsNewSession    bool
}

// CheckSession checks if a new session is allowed for the user
func (m *SessionManager) CheckSession(userID, sessionID, clientIP string, maxConcurrent int) *SessionResult {
	result := &SessionResult{
		UserID:        userID,
		SessionID:     sessionID,
		Allowed:       false,
		MaxConcurrent: maxConcurrent,
		IsNewSession:  false,
	}

	// Get or create session cache for user
	sessionCache := m.cache.GetOrCreateSessionCache(userID)

	// Check if session already exists using exported method
	if sessionCache.HasSession(sessionID) {
		// Update last seen time
		sessionCache.UpdateSessionLastSeen(sessionID)
		result.Allowed = true
		result.IsNewSession = false
		result.CurrentCount = sessionCache.GetActiveSessionCount(m.window)
		return result
	}

	// Count active sessions within the window
	activeCount := sessionCache.GetActiveSessionCount(m.window)
	result.CurrentCount = activeCount

	// Check if we can add a new session
	if maxConcurrent > 0 && activeCount >= maxConcurrent {
		result.Allowed = false
		result.SessionLimitHit = true
		result.Reason = "max concurrent sessions exceeded"
		m.logger.Warn("session limit exceeded",
			zap.String("user_id", userID),
			zap.Int("current", activeCount),
			zap.Int("max", maxConcurrent),
		)
		return result
	}

	result.Allowed = true
	result.IsNewSession = true
	return result
}

// AddSession adds a new session for a user
func (m *SessionManager) AddSession(userID, sessionID, clientIP string, geoData *domain.GeoData) {
	ipHash := m.hashIP(clientIP)

	sessionCache := m.cache.GetOrCreateSessionCache(userID)

	country := ""
	city := ""
	isp := ""
	if geoData != nil {
		country = geoData.Country
		city = geoData.City
		isp = geoData.ISP
	}

	sessionCache.AddSession(sessionID, ipHash, country, city, isp)

	m.logger.Debug("session added",
		zap.String("user_id", userID),
		zap.String("session_id", sessionID),
		zap.String("country", country),
	)
}

// RemoveSession removes a session
func (m *SessionManager) RemoveSession(userID, sessionID string) {
	sessionCache := m.cache.GetOrCreateSessionCache(userID)
	sessionCache.RemoveSession(sessionID)

	m.logger.Debug("session removed",
		zap.String("user_id", userID),
		zap.String("session_id", sessionID),
	)
}

// GetActiveSessionCount returns the number of active sessions for a user
func (m *SessionManager) GetActiveSessionCount(userID string) int {
	sessionCache := m.cache.GetOrCreateSessionCache(userID)
	return sessionCache.GetActiveSessionCount(m.window)
}

// GetUserSessions returns all sessions for a user
func (m *SessionManager) GetUserSessions(userID string) []*cache.SessionEntry {
	sessionCache := m.cache.GetOrCreateSessionCache(userID)
	return sessionCache.GetSessions()
}

// CleanupStaleSessions removes sessions that haven't been seen within the window
func (m *SessionManager) CleanupStaleSessions() int {
	count := 0

	m.cache.RangeAllSessions(func(userID string, sessionCache *cache.SessionCache) bool {
		sessionCache.RemoveStaleSessions(m.window, &count)
		return true
	})

	if count > 0 {
		m.logger.Debug("cleaned up stale sessions", zap.Int("count", count))
	}

	return count
}

// hashIP hashes an IP address for privacy (zero raw IP retention)
func (m *SessionManager) hashIP(ip string) string {
	if ip == "" {
		return ""
	}

	hash := sha256.Sum256([]byte(ip + time.Now().Format("2006-01-02"))) // Daily rotating salt
	return hex.EncodeToString(hash[:16])                                // Use first 16 bytes for shorter hash
}
