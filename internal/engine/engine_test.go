package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/eventstore"
	"github.com/hiddify/hue-go/internal/storage/cache"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	"go.uber.org/zap"
)

type capturingEventStore struct {
	events []*domain.Event
}

func (s *capturingEventStore) Store(event *domain.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *capturingEventStore) GetEvents(eventType *domain.EventType, userID *string, limit int) ([]*domain.Event, error) {
	out := make([]*domain.Event, 0, len(s.events))
	for _, ev := range s.events {
		if eventType != nil && ev.Type != *eventType {
			continue
		}
		if userID != nil {
			if ev.UserID == nil || *ev.UserID != *userID {
				continue
			}
		}
		out = append(out, ev)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *capturingEventStore) GetAllEvents(limit int) ([]*domain.Event, error) {
	if limit <= 0 || limit >= len(s.events) {
		out := make([]*domain.Event, len(s.events))
		copy(out, s.events)
		return out, nil
	}
	out := make([]*domain.Event, limit)
	copy(out, s.events[:limit])
	return out, nil
}

func (s *capturingEventStore) Close() error {
	return nil
}

var _ eventstore.EventStore = (*capturingEventStore)(nil)

type testEngineFixture struct {
	cache     *cache.MemoryCache
	userDB    *sqlite.UserDB
	events    *capturingEventStore
	quota     *QuotaEngine
	session   *SessionManager
	penalty   *PenaltyHandler
	engine    *Engine
	userID    string
	packageID string
	nodeID    string
	serviceID string
}

func newTestEngineFixture(t *testing.T, maxConcurrent int, totalTraffic int64) *testEngineFixture {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "hue-test.db")
	userDB, err := sqlite.NewUserDB("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("create user DB: %v", err)
	}
	t.Cleanup(func() {
		_ = userDB.Close()
	})

	if err := userDB.Migrate(); err != nil {
		t.Fatalf("migrate user DB: %v", err)
	}

	userID := "user-1"
	packageID := "pkg-1"
	nodeID := "node-1"
	serviceID := "svc-1"

	if err := userDB.CreateNode(&domain.Node{
		ID:                nodeID,
		SecretKey:         "node-secret",
		Name:              "node-main",
		TrafficMultiplier: 1,
		ResetMode:         domain.ResetModeNoReset,
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	if err := userDB.CreateService(&domain.Service{
		ID:                 serviceID,
		SecretKey:          "service-secret",
		NodeID:             nodeID,
		Name:               "vless",
		Protocol:           "vless",
		AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodUUID},
	}); err != nil {
		t.Fatalf("create service: %v", err)
	}

	if err := userDB.CreatePackage(&domain.Package{
		ID:            packageID,
		UserID:        userID,
		TotalTraffic:  totalTraffic,
		UploadLimit:   0,
		DownloadLimit: 0,
		ResetMode:     domain.ResetModeNoReset,
		Duration:      3600,
		MaxConcurrent: maxConcurrent,
		Status:        domain.PackageStatusActive,
	}); err != nil {
		t.Fatalf("create package: %v", err)
	}

	if err := userDB.CreateUser(&domain.User{
		ID:              userID,
		Username:        "tester",
		Password:        "secret",
		Status:          domain.UserStatusActive,
		ActivePackageID: &packageID,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	memoryCache := cache.NewMemoryCache()
	eventStore := &capturingEventStore{}
	logger := zap.NewNop()

	quota := NewQuotaEngine(userDB, nil, memoryCache, logger)
	session := NewSessionManager(memoryCache, 2*time.Second, logger)
	penalty := NewPenaltyHandler(memoryCache, 75*time.Millisecond, logger)

	eng := NewEngine(quota, session, penalty, nil, eventStore, memoryCache, userDB, logger)

	return &testEngineFixture{
		cache:     memoryCache,
		userDB:    userDB,
		events:    eventStore,
		quota:     quota,
		session:   session,
		penalty:   penalty,
		engine:    eng,
		userID:    userID,
		packageID: packageID,
		nodeID:    nodeID,
		serviceID: serviceID,
	}
}

func TestProcessUsageReport_AcceptsAndRecordsUsage(t *testing.T) {
	fx := newTestEngineFixture(t, 2, 1_000)

	result := fx.engine.ProcessUsageReport(&domain.UsageReport{
		UserID:    fx.userID,
		NodeID:    fx.nodeID,
		ServiceID: fx.serviceID,
		SessionID: "s1",
		ClientIP:  "1.2.3.4",
		Upload:    120,
		Download:  80,
		Tags:      []string{"vless"},
		Timestamp: time.Now(),
	})

	if !result.Accepted {
		t.Fatalf("expected report to be accepted, got reason=%q", result.Reason)
	}
	if result.PackageID != fx.packageID {
		t.Fatalf("expected package %s, got %s", fx.packageID, result.PackageID)
	}

	pkg, err := fx.userDB.GetPackage(fx.packageID)
	if err != nil {
		t.Fatalf("get package: %v", err)
	}
	if pkg.CurrentUpload != 120 || pkg.CurrentDownload != 80 || pkg.CurrentTotal != 200 {
		t.Fatalf("unexpected package counters: upload=%d download=%d total=%d", pkg.CurrentUpload, pkg.CurrentDownload, pkg.CurrentTotal)
	}

	node, err := fx.userDB.GetNode(fx.nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.CurrentUpload != 120 || node.CurrentDownload != 80 {
		t.Fatalf("unexpected node counters: upload=%d download=%d", node.CurrentUpload, node.CurrentDownload)
	}

	svc, err := fx.userDB.GetService(fx.serviceID)
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if svc.CurrentUpload != 120 || svc.CurrentDownload != 80 {
		t.Fatalf("unexpected service counters: upload=%d download=%d", svc.CurrentUpload, svc.CurrentDownload)
	}

	if got := fx.session.GetActiveSessionCount(fx.userID); got != 1 {
		t.Fatalf("expected 1 active session, got %d", got)
	}

	if len(fx.events.events) != 2 {
		t.Fatalf("expected 2 emitted events, got %d", len(fx.events.events))
	}
	if fx.events.events[0].Type != domain.EventUserConnected {
		t.Fatalf("expected first event USER_CONNECTED, got %s", fx.events.events[0].Type)
	}
	if fx.events.events[1].Type != domain.EventUsageRecorded {
		t.Fatalf("expected second event USAGE_RECORDED, got %s", fx.events.events[1].Type)
	}
}

func TestProcessUsageReport_AppliesPenaltyOnConcurrentLimit(t *testing.T) {
	fx := newTestEngineFixture(t, 1, 5_000)

	first := fx.engine.ProcessUsageReport(&domain.UsageReport{
		UserID:    fx.userID,
		NodeID:    fx.nodeID,
		ServiceID: fx.serviceID,
		SessionID: "s1",
		ClientIP:  "10.0.0.1",
		Upload:    10,
		Download:  10,
		Timestamp: time.Now(),
	})
	if !first.Accepted {
		t.Fatalf("expected first report to be accepted, got reason=%q", first.Reason)
	}

	second := fx.engine.ProcessUsageReport(&domain.UsageReport{
		UserID:    fx.userID,
		NodeID:    fx.nodeID,
		ServiceID: fx.serviceID,
		SessionID: "s2",
		ClientIP:  "10.0.0.2",
		Upload:    15,
		Download:  5,
		Timestamp: time.Now(),
	})

	if second.Accepted {
		t.Fatalf("expected second report to be rejected")
	}
	if !second.PenaltyApplied || !second.ShouldDisconnect {
		t.Fatalf("expected penalty and disconnect, got penalty=%v disconnect=%v", second.PenaltyApplied, second.ShouldDisconnect)
	}

	pen := fx.penalty.CheckPenalty(fx.userID)
	if !pen.HasPenalty {
		t.Fatalf("expected active penalty")
	}

	batch := fx.engine.GetDisconnectBatch()
	if len(batch) != 1 {
		t.Fatalf("expected 1 disconnect command, got %d", len(batch))
	}
	if batch[0].UserID != fx.userID || batch[0].SessionID != "s1" {
		t.Fatalf("unexpected disconnect command: user=%s session=%s", batch[0].UserID, batch[0].SessionID)
	}

	last := fx.events.events[len(fx.events.events)-1]
	if last.Type != domain.EventPenaltyApplied {
		t.Fatalf("expected last event PENALTY_APPLIED, got %s", last.Type)
	}

	third := fx.engine.ProcessUsageReport(&domain.UsageReport{
		UserID:    fx.userID,
		NodeID:    fx.nodeID,
		ServiceID: fx.serviceID,
		SessionID: "s3",
		ClientIP:  "10.0.0.3",
		Upload:    1,
		Download:  1,
		Timestamp: time.Now(),
	})

	if third.Accepted {
		t.Fatalf("expected report to be rejected while penalty is active")
	}
	if !third.ShouldDisconnect {
		t.Fatalf("expected disconnect while penalty is active")
	}
}

func TestProcessUsageReport_QuotaExceededSuspendsUser(t *testing.T) {
	fx := newTestEngineFixture(t, 2, 100)

	result := fx.engine.ProcessUsageReport(&domain.UsageReport{
		UserID:    fx.userID,
		NodeID:    fx.nodeID,
		ServiceID: fx.serviceID,
		SessionID: "s1",
		ClientIP:  "172.20.10.9",
		Upload:    70,
		Download:  40,
		Timestamp: time.Now(),
	})

	if result.Accepted {
		t.Fatalf("expected quota-exceeded report to be rejected")
	}
	if !result.QuotaExceeded || !result.ShouldDisconnect {
		t.Fatalf("expected quota exceeded + disconnect, got quota=%v disconnect=%v", result.QuotaExceeded, result.ShouldDisconnect)
	}

	user, err := fx.userDB.GetUser(fx.userID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.Status != domain.UserStatusSuspended {
		t.Fatalf("expected user status suspended, got %s", user.Status)
	}

	if got := fx.session.GetActiveSessionCount(fx.userID); got != 0 {
		t.Fatalf("expected no active session because report was rejected, got %d", got)
	}

	last := fx.events.events[len(fx.events.events)-1]
	if last.Type != domain.EventUserSuspended {
		t.Fatalf("expected last event USER_SUSPENDED, got %s", last.Type)
	}
}

func TestCleanup_RemovesExpiredPenaltiesAndStaleSessions(t *testing.T) {
	fx := newTestEngineFixture(t, 2, 1_000)

	fx.session.AddSession(fx.userID, "old-session", "192.168.1.5", nil)
	fx.cache.RangeSessions(fx.userID, func(sessionID string, session *cache.SessionEntry) bool {
		session.LastSeenAt = time.Now().Add(-3 * time.Second)
		return true
	})

	fx.penalty.ApplyPenalty(fx.userID, "test")
	time.Sleep(90 * time.Millisecond)

	fx.engine.Cleanup()

	if got := fx.session.GetActiveSessionCount(fx.userID); got != 0 {
		t.Fatalf("expected stale sessions to be cleaned up, got %d", got)
	}

	if p := fx.penalty.CheckPenalty(fx.userID); p.HasPenalty {
		t.Fatalf("expected expired penalty to be cleaned up")
	}
}

func TestQuotaEngine_CheckAndEnforceQuota_QueuesDisconnectOnExceeded(t *testing.T) {
	fx := newTestEngineFixture(t, 2, 100)

	if err := fx.userDB.UpdatePackageUsage(fx.packageID, 100, 0); err != nil {
		t.Fatalf("set initial package usage: %v", err)
	}

	if _, err := fx.quota.CheckAndEnforceQuota(fx.userID); err != nil {
		t.Fatalf("check and enforce quota on exceeded: %v", err)
	}

	user, err := fx.userDB.GetUser(fx.userID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if user.Status != domain.UserStatusSuspended {
		t.Fatalf("expected user status suspended, got %s", user.Status)
	}

	batch := fx.engine.GetDisconnectBatch()
	if len(batch) == 0 {
		t.Fatalf("expected at least one disconnect command")
	}
	if batch[0].UserID != fx.userID || batch[0].Reason != "quota_exceeded" {
		t.Fatalf("unexpected disconnect command: user=%s reason=%s", batch[0].UserID, batch[0].Reason)
	}
}
