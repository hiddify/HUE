package engine

import (
	"fmt"
	"sync"

	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/storage/cache"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	"go.uber.org/zap"
)

// QuotaEngine handles quota enforcement and usage tracking
type QuotaEngine struct {
	userDB   *sqlite.UserDB
	activeDB *sqlite.ActiveDB
	cache    *cache.MemoryCache
	logger   *zap.Logger
	managerEnforcementMode domain.EnforcementMode

	// Fine-grained locks per user
	userLocks sync.Map // map[string]*sync.RWMutex
}

// NewQuotaEngine creates a new QuotaEngine instance
func NewQuotaEngine(userDB *sqlite.UserDB, activeDB *sqlite.ActiveDB, cache *cache.MemoryCache, logger *zap.Logger) *QuotaEngine {
	return &QuotaEngine{
		userDB:   userDB,
		activeDB: activeDB,
		cache:    cache,
		logger:   logger,
		managerEnforcementMode: domain.EnforcementModeDefault,
	}
}

func (e *QuotaEngine) SetManagerEnforcementMode(mode domain.EnforcementMode) {
	switch mode {
	case domain.EnforcementModeSoft, domain.EnforcementModeDefault, domain.EnforcementModeHard:
		e.managerEnforcementMode = mode
	default:
		e.managerEnforcementMode = domain.EnforcementModeDefault
	}
}

// getUserLock gets or creates a lock for a specific user
func (e *QuotaEngine) getUserLock(userID string) *sync.RWMutex {
	if v, ok := e.userLocks.Load(userID); ok {
		return v.(*sync.RWMutex)
	}

	lock := &sync.RWMutex{}
	actual, _ := e.userLocks.LoadOrStore(userID, lock)
	return actual.(*sync.RWMutex)
}

// CheckQuota checks if a user can use the specified amount of traffic
func (e *QuotaEngine) CheckQuota(userID string, upload, download int64) (*QuotaResult, error) {
	lock := e.getUserLock(userID)
	lock.RLock()
	defer lock.RUnlock()

	result := &QuotaResult{
		UserID: userID,
		CanUse: false,
		Reason: "",
		Pkg:    nil,
		Cached: false,
	}

	// Check cache first
	cachedUser := e.cache.GetUser(userID)
	if cachedUser != nil {
		result.Cached = true

		// Check user status
		if cachedUser.Status != domain.UserStatusActive {
			result.Reason = fmt.Sprintf("user status is %s", cachedUser.Status)
			return result, nil
		}

		// Check if user has active package
		if cachedUser.ActivePackageID == nil {
			result.Reason = "no active package"
			return result, nil
		}

		// Check traffic quota from cache
		pkg, err := e.userDB.GetPackage(*cachedUser.ActivePackageID)
		if err != nil {
			return nil, err
		}
		if pkg == nil {
			result.Reason = "package not found"
			return result, nil
		}

		result.Pkg = pkg

		// Check if package is active
		if !pkg.IsActive() {
			result.Reason = fmt.Sprintf("package status is %s", pkg.Status)
			return result, nil
		}

		// Check expiry
		if pkg.IsExpired() {
			result.Reason = "package expired"
			return result, nil
		}

		// Check total traffic
		if pkg.TotalTraffic > 0 {
			projectedTotal := cachedUser.CurrentTotal + upload + download
			if projectedTotal > pkg.TotalTraffic {
				result.Reason = "total traffic quota exceeded"
				result.QuotaExceeded = true
				return result, nil
			}
		}

		// Check upload limit
		if pkg.UploadLimit > 0 {
			projectedUpload := cachedUser.CurrentUpload + upload
			if projectedUpload > pkg.UploadLimit {
				result.Reason = "upload quota exceeded"
				result.QuotaExceeded = true
				return result, nil
			}
		}

		// Check download limit
		if pkg.DownloadLimit > 0 {
			projectedDownload := cachedUser.CurrentDownload + download
			if projectedDownload > pkg.DownloadLimit {
				result.Reason = "download quota exceeded"
				result.QuotaExceeded = true
				return result, nil
			}
		}

		result.CanUse = true

		mgrRes, err := e.checkManagerLimitsByUserID(userID, upload, download, 0, 0, 0)
		if err != nil {
			return nil, err
		}
		if mgrRes != nil && !mgrRes.Allowed {
			result.QuotaExceeded = true
			result.Reason = mgrRes.Reason
			if e.managerEnforcementMode == domain.EnforcementModeSoft {
				result.CanUse = true
			} else {
				result.CanUse = false
			}
		}
		return result, nil
	}

	// Cache miss - load from database
	user, err := e.userDB.GetUser(userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		result.Reason = "user not found"
		return result, nil
	}

	// Update cache
	e.cache.SetUser(userID, user.Status, user.ActivePackageID, 0)

	// Check user status
	if !user.CanConnect() {
		result.Reason = fmt.Sprintf("user cannot connect: status=%s", user.Status)
		return result, nil
	}

	// Get package
	pkg, err := e.userDB.GetPackageByUserID(userID)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		result.Reason = "no active package"
		return result, nil
	}

	result.Pkg = pkg

	// Update cache with max concurrent
	e.cache.SetUser(userID, user.Status, user.ActivePackageID, pkg.MaxConcurrent)

	// Check package status
	if !pkg.CanUse() {
		result.Reason = fmt.Sprintf("package cannot be used: status=%s, expired=%v", pkg.Status, pkg.IsExpired())
		return result, nil
	}

	// Check traffic limits
	if !e.checkTrafficLimits(pkg, upload, download) {
		result.Reason = "traffic quota exceeded"
		result.QuotaExceeded = true
		return result, nil
	}

	result.CanUse = true
	mgrRes, err := e.checkManagerLimitsByUser(user, upload, download, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	if mgrRes != nil && !mgrRes.Allowed {
		result.QuotaExceeded = true
		result.Reason = mgrRes.Reason
		if e.managerEnforcementMode != domain.EnforcementModeSoft {
			result.CanUse = false
		}
	}
	return result, nil
}

// RecordUsage records usage for a user and updates quotas
func (e *QuotaEngine) RecordUsage(userID string, upload, download int64) error {
	lock := e.getUserLock(userID)
	lock.Lock()
	defer lock.Unlock()

	// Get package
	pkg, err := e.userDB.GetPackageByUserID(userID)
	if err != nil {
		return err
	}
	if pkg == nil {
		return fmt.Errorf("no active package for user %s", userID)
	}

	// Update package usage in database
	if err := e.userDB.UpdatePackageUsage(pkg.ID, upload, download); err != nil {
		return err
	}

	user, err := e.userDB.GetUser(userID)
	if err != nil {
		return err
	}
	if user != nil && user.ManagerID != nil {
		if err := e.userDB.ApplyManagerUsageDelta(*user.ManagerID, upload, download, 0, 0, 0); err != nil {
			return err
		}
	}

	// Update cache
	e.cache.UpdateUserUsage(userID, upload, download)

	// Update last connection
	if err := e.userDB.UpdateUserLastConnection(userID); err != nil {
		e.logger.Warn("failed to update last connection", zap.String("user_id", userID), zap.Error(err))
	}

	// Check if quota exceeded after update
	pkg, _ = e.userDB.GetPackage(pkg.ID)
	if pkg != nil && !pkg.HasTrafficRemaining() {
		// Mark package as finished
		if err := e.userDB.UpdatePackageStatus(pkg.ID, domain.PackageStatusFinish); err != nil {
			e.logger.Error("failed to mark package as finished", zap.String("package_id", pkg.ID), zap.Error(err))
		}
		// Suspend user
		if err := e.userDB.UpdateUserStatus(userID, domain.UserStatusFinish); err != nil {
			e.logger.Error("failed to suspend user", zap.String("user_id", userID), zap.Error(err))
		}
		// Update cache
		e.cache.SetUser(userID, domain.UserStatusFinish, &pkg.ID, pkg.MaxConcurrent)
	}

	e.logger.Debug("usage recorded",
		zap.String("user_id", userID),
		zap.Int64("upload", upload),
		zap.Int64("download", download),
	)

	return nil
}

func (e *QuotaEngine) CheckManagerSessionLimits(userID string, sessionDelta, onlineUsersDelta, activeUsersDelta int64) (*sqlite.ManagerLimitCheckResult, error) {
	return e.checkManagerLimitsByUserID(userID, 0, 0, sessionDelta, onlineUsersDelta, activeUsersDelta)
}

func (e *QuotaEngine) RecordManagerSessionDelta(userID string, sessionDelta, onlineUsersDelta, activeUsersDelta int64) error {
	if sessionDelta == 0 && onlineUsersDelta == 0 && activeUsersDelta == 0 {
		return nil
	}
	user, err := e.userDB.GetUser(userID)
	if err != nil {
		return err
	}
	if user == nil || user.ManagerID == nil {
		return nil
	}
	return e.userDB.ApplyManagerUsageDelta(*user.ManagerID, 0, 0, sessionDelta, onlineUsersDelta, activeUsersDelta)
}

func (e *QuotaEngine) checkManagerLimitsByUserID(userID string, upload, download, sessionDelta, onlineUsersDelta, activeUsersDelta int64) (*sqlite.ManagerLimitCheckResult, error) {
	user, err := e.userDB.GetUser(userID)
	if err != nil {
		return nil, err
	}
	return e.checkManagerLimitsByUser(user, upload, download, sessionDelta, onlineUsersDelta, activeUsersDelta)
}

func (e *QuotaEngine) checkManagerLimitsByUser(user *domain.User, upload, download, sessionDelta, onlineUsersDelta, activeUsersDelta int64) (*sqlite.ManagerLimitCheckResult, error) {
	if user == nil || user.ManagerID == nil || *user.ManagerID == "" {
		return &sqlite.ManagerLimitCheckResult{Allowed: true}, nil
	}

	res, err := e.userDB.CheckManagerLimits(*user.ManagerID, upload, download, sessionDelta, onlineUsersDelta, activeUsersDelta)
	if err != nil {
		return nil, err
	}
	if !res.Allowed {
		e.logger.Warn("manager limit reached",
			zap.String("manager_id", res.ManagerID),
			zap.String("reason", res.Reason),
			zap.String("mode", string(e.managerEnforcementMode)),
		)
	}
	return res, nil
}

// CheckAndEnforceQuota checks quota and enforces limits
func (e *QuotaEngine) CheckAndEnforceQuota(userID string) (*QuotaResult, error) {
	result, err := e.CheckQuota(userID, 0, 0)
	if err != nil {
		return nil, err
	}

	pkg := result.Pkg
	if pkg == nil {
		pkg, err = e.userDB.GetPackageByUserID(userID)
		if err != nil {
			return nil, err
		}
	}

	if pkg != nil {
		totalExceeded := pkg.TotalTraffic > 0 && pkg.CurrentTotal >= pkg.TotalTraffic
		uploadExceeded := pkg.UploadLimit > 0 && pkg.CurrentUpload >= pkg.UploadLimit
		downloadExceeded := pkg.DownloadLimit > 0 && pkg.CurrentDownload >= pkg.DownloadLimit

		if totalExceeded || uploadExceeded || downloadExceeded {
			result.CanUse = false
			result.QuotaExceeded = true
			result.Reason = "traffic quota exceeded"
		}
	}

	if !result.CanUse && result.QuotaExceeded {
		// Suspend user
		if err := e.userDB.UpdateUserStatus(userID, domain.UserStatusSuspended); err != nil {
			e.logger.Error("failed to suspend user", zap.String("user_id", userID), zap.Error(err))
		}

		// Queue disconnect
		e.cache.QueueDisconnect(userID, "", "quota_exceeded", "")
	}

	return result, nil
}

// RefreshCache refreshes the cache for a user
func (e *QuotaEngine) RefreshCache(userID string) error {
	user, err := e.userDB.GetUser(userID)
	if err != nil {
		return err
	}
	if user == nil {
		e.cache.DeleteUser(userID)
		return nil
	}

	pkg, _ := e.userDB.GetPackageByUserID(userID)
	maxConcurrent := 1
	if pkg != nil {
		maxConcurrent = pkg.MaxConcurrent
	}

	e.cache.SetUser(userID, user.Status, user.ActivePackageID, maxConcurrent)
	return nil
}

// checkTrafficLimits checks if the traffic limits are exceeded
func (e *QuotaEngine) checkTrafficLimits(pkg *domain.Package, upload, download int64) bool {
	// Check total traffic
	if pkg.TotalTraffic > 0 {
		if pkg.CurrentTotal+upload+download > pkg.TotalTraffic {
			return false
		}
	}

	// Check upload limit
	if pkg.UploadLimit > 0 {
		if pkg.CurrentUpload+upload > pkg.UploadLimit {
			return false
		}
	}

	// Check download limit
	if pkg.DownloadLimit > 0 {
		if pkg.CurrentDownload+download > pkg.DownloadLimit {
			return false
		}
	}

	return true
}

// QuotaResult represents the result of a quota check
type QuotaResult struct {
	UserID        string
	CanUse        bool
	Reason        string
	QuotaExceeded bool
	Pkg           *domain.Package
	Cached        bool
}
