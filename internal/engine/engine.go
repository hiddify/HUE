package engine

import (
	"time"

	"github.com/google/uuid"
	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/eventstore"
	"github.com/hiddify/hue-go/internal/storage/cache"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	"go.uber.org/zap"
)

// Engine is the main usage processing engine that coordinates all components
type Engine struct {
	quota    *QuotaEngine
	session  *SessionManager
	penalty  *PenaltyHandler
	geo      *GeoHandler
	events   eventstore.EventStore
	cache    *cache.MemoryCache
	userDB   *sqlite.UserDB
	logger   *zap.Logger
}

// NewEngine creates a new Engine instance
func NewEngine(
	quota *QuotaEngine,
	session *SessionManager,
	penalty *PenaltyHandler,
	geo *GeoHandler,
	events eventstore.EventStore,
	cache *cache.MemoryCache,
	userDB *sqlite.UserDB,
	logger *zap.Logger,
) *Engine {
	return &Engine{
		quota:   quota,
		session: session,
		penalty: penalty,
		geo:     geo,
		events:  events,
		cache:   cache,
		userDB:  userDB,
		logger:  logger,
	}
}

// ProcessUsageReport processes a usage report from a node/service
func (e *Engine) ProcessUsageReport(report *domain.UsageReport) *domain.UsageReportResult {
	result := &domain.UsageReportResult{
		UserID:    report.UserID,
		Accepted:  false,
	}

	// 1. Check penalty first
	penaltyResult := e.penalty.CheckPenalty(report.UserID)
	if penaltyResult.HasPenalty {
		result.ShouldDisconnect = true
		result.Reason = "user has active penalty"
		return result
	}

	// 2. Get user's package for max concurrent
	pkg, err := e.userDB.GetPackageByUserID(report.UserID)
	if err != nil {
		result.Reason = "failed to get package"
		e.logger.Error("failed to get package", zap.String("user_id", report.UserID), zap.Error(err))
		return result
	}
	if pkg == nil {
		result.Reason = "no active package"
		return result
	}

	// 3. Check/validate session
	sessionResult := e.session.CheckSession(report.UserID, report.SessionID, report.ClientIP, pkg.MaxConcurrent)

	if sessionResult.SessionLimitHit {
		// Apply penalty
		e.penalty.ApplyPenalty(report.UserID, "concurrent_session_limit_exceeded")
		result.PenaltyApplied = true
		result.ShouldDisconnect = true
		result.Reason = "concurrent session limit exceeded, penalty applied"

		// Emit event
		e.emitEvent(domain.EventPenaltyApplied, &report.UserID, &pkg.ID, nil, nil, []string{"concurrent_limit"})
		return result
	}

	// 4. Check quota
	quotaResult, err := e.quota.CheckQuota(report.UserID, report.Upload, report.Download)
	if err != nil {
		result.Reason = "quota check failed"
		e.logger.Error("quota check failed", zap.String("user_id", report.UserID), zap.Error(err))
		return result
	}

	if !quotaResult.CanUse {
		result.QuotaExceeded = quotaResult.QuotaExceeded
		result.ShouldDisconnect = true
		result.Reason = quotaResult.Reason

		// Suspend user if quota exceeded
		if quotaResult.QuotaExceeded {
			e.userDB.UpdateUserStatus(report.UserID, domain.UserStatusSuspended)
			e.emitEvent(domain.EventUserSuspended, &report.UserID, &pkg.ID, nil, nil, []string{"quota_exceeded"})
		}
		return result
	}

	// 5. Extract geo data (IP is discarded after this)
	var geoData *domain.GeoData
	if e.geo != nil && e.geo.IsReady() && report.ClientIP != "" {
		geoData = e.geo.ExtractGeo(report.ClientIP)
	}

	// 6. Add/update session
	if sessionResult.IsNewSession {
		e.session.AddSession(report.UserID, report.SessionID, report.ClientIP, geoData)
		e.emitEvent(domain.EventUserConnected, &report.UserID, &pkg.ID, &report.NodeID, &report.ServiceID, report.Tags)
	} else {
		e.session.AddSession(report.UserID, report.SessionID, report.ClientIP, geoData)
	}

	// 7. Record usage
	if err := e.quota.RecordUsage(report.UserID, report.Upload, report.Download); err != nil {
		result.Reason = "failed to record usage"
		e.logger.Error("failed to record usage", zap.String("user_id", report.UserID), zap.Error(err))
		return result
	}

	// 8. Update node and service usage
	if err := e.userDB.UpdateNodeUsage(report.NodeID, report.Upload, report.Download); err != nil {
		e.logger.Warn("failed to update node usage", zap.String("node_id", report.NodeID), zap.Error(err))
	}
	if err := e.userDB.UpdateServiceUsage(report.ServiceID, report.Upload, report.Download); err != nil {
		e.logger.Warn("failed to update service usage", zap.String("service_id", report.ServiceID), zap.Error(err))
	}

	// 9. Emit usage recorded event
	e.emitEvent(domain.EventUsageRecorded, &report.UserID, &pkg.ID, &report.NodeID, &report.ServiceID, report.Tags)

	// 10. Check if package should be finished
	updatedPkg, _ := e.userDB.GetPackage(pkg.ID)
	if updatedPkg != nil && !updatedPkg.HasTrafficRemaining() {
		e.userDB.UpdatePackageStatus(pkg.ID, domain.PackageStatusFinish)
		e.userDB.UpdateUserStatus(report.UserID, domain.UserStatusFinish)
		e.emitEvent(domain.EventPackageExpired, &report.UserID, &pkg.ID, nil, nil, nil)
	}

	result.Accepted = true
	result.PackageID = pkg.ID
	return result
}

// HandleUserDisconnect handles a user disconnection
func (e *Engine) HandleUserDisconnect(userID, sessionID string) {
	e.session.RemoveSession(userID, sessionID)

	// Emit disconnect event
	e.emitEvent(domain.EventUserDisconnected, &userID, nil, nil, nil, nil)
}

// GetDisconnectBatch returns pending disconnect commands
func (e *Engine) GetDisconnectBatch() []*cache.DisconnectCommand {
	return e.cache.GetDisconnectBatch()
}

// Cleanup performs periodic cleanup tasks
func (e *Engine) Cleanup() {
	// Cleanup stale sessions
	sessionCount := e.session.CleanupStaleSessions()

	// Cleanup expired penalties
	penaltyCount := e.penalty.CleanupExpiredPenalties()

	if sessionCount > 0 || penaltyCount > 0 {
		e.logger.Info("cleanup completed",
			zap.Int("stale_sessions", sessionCount),
			zap.Int("expired_penalties", penaltyCount),
		)
	}
}

// emitEvent emits an event to the event store
func (e *Engine) emitEvent(eventType domain.EventType, userID, packageID, nodeID, serviceID *string, tags []string) {
	if e.events == nil {
		return
	}

	event := &domain.Event{
		ID:        uuid.New().String(),
		Type:      eventType,
		UserID:    userID,
		PackageID: packageID,
		NodeID:    nodeID,
		ServiceID: serviceID,
		Tags:      tags,
		Timestamp: time.Now(),
	}

	if err := e.events.Store(event); err != nil {
		e.logger.Error("failed to store event",
			zap.String("type", string(eventType)),
			zap.Error(err),
		)
	}
}
