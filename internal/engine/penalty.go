package engine

import (
	"time"

	"github.com/hiddify/hue-go/internal/storage/cache"
	"go.uber.org/zap"
)

// PenaltyHandler handles temporary penalties for concurrent session violations
type PenaltyHandler struct {
	cache    *cache.MemoryCache
	duration time.Duration
	logger   *zap.Logger
}

// NewPenaltyHandler creates a new PenaltyHandler instance
func NewPenaltyHandler(cache *cache.MemoryCache, duration time.Duration, logger *zap.Logger) *PenaltyHandler {
	return &PenaltyHandler{
		cache:    cache,
		duration: duration,
		logger:   logger,
	}
}

// PenaltyResult represents the result of a penalty check
type PenaltyResult struct {
	UserID     string
	HasPenalty bool
	Reason     string
	ExpiresAt  time.Time
	TimeLeft   time.Duration
}

// CheckPenalty checks if a user has an active penalty
func (h *PenaltyHandler) CheckPenalty(userID string) *PenaltyResult {
	result := &PenaltyResult{
		UserID:     userID,
		HasPenalty: false,
	}

	penalty := h.cache.GetPenalty(userID)
	if penalty == nil {
		return result
	}

	result.HasPenalty = true
	result.Reason = penalty.Reason
	result.ExpiresAt = penalty.ExpiresAt
	result.TimeLeft = time.Until(penalty.ExpiresAt)

	h.logger.Debug("penalty check",
		zap.String("user_id", userID),
		zap.Bool("has_penalty", true),
		zap.Duration("time_left", result.TimeLeft),
	)

	return result
}

// ApplyPenalty applies a penalty to a user
func (h *PenaltyHandler) ApplyPenalty(userID, reason string) {
	h.cache.SetPenalty(userID, reason, h.duration)

	// Queue disconnect for all sessions
	sessions := h.cache.GetOrCreateSessionCache(userID).GetSessions()
	for _, session := range sessions {
		h.cache.QueueDisconnect(userID, session.SessionID, reason, "")
	}

	h.logger.Warn("penalty applied",
		zap.String("user_id", userID),
		zap.String("reason", reason),
		zap.Duration("duration", h.duration),
	)
}

// ClearPenalty clears a penalty for a user
func (h *PenaltyHandler) ClearPenalty(userID string) {
	h.cache.ClearPenalty(userID)

	h.logger.Info("penalty cleared", zap.String("user_id", userID))
}

// GetExpiredPenalties returns user IDs with expired penalties
func (h *PenaltyHandler) GetExpiredPenalties() []string {
	var expired []string

	h.cache.RangePenalties(func(userID string, penalty *cache.PenaltyEntry) bool {
		if time.Now().After(penalty.ExpiresAt) {
			expired = append(expired, userID)
		}
		return true
	})

	return expired
}

// CleanupExpiredPenalties removes expired penalties
func (h *PenaltyHandler) CleanupExpiredPenalties() int {
	expired := h.GetExpiredPenalties()
	for _, userID := range expired {
		h.cache.ClearPenalty(userID)
	}

	if len(expired) > 0 {
		h.logger.Debug("cleaned up expired penalties", zap.Int("count", len(expired)))
	}

	return len(expired)
}
