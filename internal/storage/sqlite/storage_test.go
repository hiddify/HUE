package sqlite

import (
	"testing"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
)

func TestActiveDBBufferFlushAndAggregation(t *testing.T) {
	db, err := NewActiveDB(":memory:")
	if err != nil {
		t.Fatalf("new active db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now()
	report := &domain.UsageReport{
		ID:        "r1",
		UserID:    "u1",
		NodeID:    "n1",
		ServiceID: "s1",
		Upload:    10,
		Download:  20,
		SessionID: "sess-1",
		Tags:      []string{"vless"},
		Timestamp: now,
	}

	if err := db.BufferUsage(report); err != nil {
		t.Fatalf("buffer usage: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	rows, err := db.GetUnprocessedReports(10)
	if err != nil {
		t.Fatalf("get unprocessed: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "r1" {
		t.Fatalf("unexpected unprocessed rows")
	}

	if err := db.MarkProcessed([]string{"r1"}); err != nil {
		t.Fatalf("mark processed: %v", err)
	}

	up, down, err := db.GetAggregatedUsage("u1", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("get aggregated usage: %v", err)
	}
	if up != 10 || down != 20 {
		t.Fatalf("unexpected aggregated usage up=%d down=%d", up, down)
	}
}

func TestHistoryDBStoreAndQuery(t *testing.T) {
	db, err := NewHistoryDB(":memory:")
	if err != nil {
		t.Fatalf("new history db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	userID := "u1"
	pkgID := "p1"
	nodeID := "n1"
	serviceID := "s1"

	event := &domain.Event{
		ID:        "e1",
		Type:      domain.EventUsageRecorded,
		UserID:    &userID,
		PackageID: &pkgID,
		NodeID:    &nodeID,
		ServiceID: &serviceID,
		Tags:      []string{"grpc"},
		Timestamp: time.Now(),
	}
	if err := db.StoreEvent(event); err != nil {
		t.Fatalf("store event: %v", err)
	}

	eventType := domain.EventUsageRecorded
	events, err := db.GetEvents(&eventType, &userID, nil, nil, 10)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != 1 || events[0].ID != "e1" {
		t.Fatalf("unexpected events query result")
	}

	if err := db.StoreUsageHistory(userID, pkgID, nodeID, serviceID, 25, 35, "sess-1", &domain.GeoData{Country: "US", City: "NY", ISP: "ISP"}, []string{"tag1"}, time.Now()); err != nil {
		t.Fatalf("store usage history: %v", err)
	}

	history, err := db.GetUsageHistory(userID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("get usage history: %v", err)
	}
	if len(history) != 1 || history[0].Upload != 25 || history[0].Download != 35 {
		t.Fatalf("unexpected usage history result")
	}
}

func TestUserDBManagerHierarchyAndPropagation(t *testing.T) {
	db, err := NewUserDB("sqlite://" + t.TempDir() + "/manager.db")
	if err != nil {
		t.Fatalf("new user db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate user db: %v", err)
	}

	root := &domain.Manager{
		ID:   "mgr-root",
		Name: "Root",
		Package: &domain.ManagerPackage{
			TotalLimit:     1000,
			UploadLimit:    600,
			DownloadLimit:  700,
			MaxSessions:    10,
			MaxOnlineUsers: 5,
			MaxActiveUsers: 5,
			Status:         domain.ManagerPackageStatusActive,
		},
	}
	if err := db.CreateManager(root); err != nil {
		t.Fatalf("create root manager: %v", err)
	}

	parentID := "mgr-root"
	child := &domain.Manager{
		ID:       "mgr-child",
		Name:     "Child",
		ParentID: &parentID,
		Package: &domain.ManagerPackage{
			TotalLimit:     500,
			UploadLimit:    300,
			DownloadLimit:  300,
			MaxSessions:    4,
			MaxOnlineUsers: 3,
			MaxActiveUsers: 3,
			Status:         domain.ManagerPackageStatusActive,
		},
	}
	if err := db.CreateManager(child); err != nil {
		t.Fatalf("create child manager: %v", err)
	}

	badChild := &domain.Manager{
		ID:       "mgr-bad",
		Name:     "Bad",
		ParentID: &parentID,
		Package: &domain.ManagerPackage{
			TotalLimit: 2000,
			Status:     domain.ManagerPackageStatusActive,
		},
	}
	if err := db.CreateManager(badChild); err == nil {
		t.Fatalf("expected child manager creation to fail when exceeding parent limits")
	}

	allowed, err := db.CheckManagerLimits("mgr-child", 100, 50, 1, 1, 1)
	if err != nil {
		t.Fatalf("check manager limits: %v", err)
	}
	if !allowed.Allowed {
		t.Fatalf("expected manager limits check to pass, reason=%s", allowed.Reason)
	}

	if err := db.ApplyManagerUsageDelta("mgr-child", 100, 50, 1, 1, 1); err != nil {
		t.Fatalf("apply manager usage delta: %v", err)
	}

	rootPkg, err := db.GetManagerPackage("mgr-root")
	if err != nil {
		t.Fatalf("get root package: %v", err)
	}
	childPkg, err := db.GetManagerPackage("mgr-child")
	if err != nil {
		t.Fatalf("get child package: %v", err)
	}

	if rootPkg.CurrentTotal != 150 || childPkg.CurrentTotal != 150 {
		t.Fatalf("expected propagated total usage to both child and root: root=%d child=%d", rootPkg.CurrentTotal, childPkg.CurrentTotal)
	}
	if rootPkg.CurrentSessions != 1 || childPkg.CurrentSessions != 1 {
		t.Fatalf("expected propagated session counters to both child and root: root=%d child=%d", rootPkg.CurrentSessions, childPkg.CurrentSessions)
	}

	denied, err := db.CheckManagerLimits("mgr-child", 1000, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("check manager limits denied case: %v", err)
	}
	if denied.Allowed {
		t.Fatalf("expected manager limits check to fail for oversized usage")
	}
}

func TestUserDBOwnerAndServiceAuthKeys(t *testing.T) {
	db, err := NewUserDB("sqlite://" + t.TempDir() + "/auth-keys.db")
	if err != nil {
		t.Fatalf("new user db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate user db: %v", err)
	}

	if err := db.UpsertOwnerAuthKey("owner-key-v1"); err != nil {
		t.Fatalf("upsert owner auth key: %v", err)
	}

	ok, err := db.ValidateOwnerAuthKey("owner-key-v1")
	if err != nil {
		t.Fatalf("validate owner key: %v", err)
	}
	if !ok {
		t.Fatalf("expected owner key to validate")
	}

	ok, err = db.ValidateOwnerAuthKey("wrong-owner-key")
	if err != nil {
		t.Fatalf("validate wrong owner key: %v", err)
	}
	if ok {
		t.Fatalf("expected wrong owner key to fail")
	}

	if err := db.CreateNode(&domain.Node{
		ID:                "n-auth",
		SecretKey:         "node-key",
		Name:              "node-auth",
		TrafficMultiplier: 1,
		ResetMode:         domain.ResetModeNoReset,
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	if err := db.CreateService(&domain.Service{
		ID:                 "s-auth",
		SecretKey:          "service-key-v1",
		NodeID:             "n-auth",
		Name:               "svc-auth",
		Protocol:           "vless",
		AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodPassword},
	}); err != nil {
		t.Fatalf("create service: %v", err)
	}

	svcOK, err := db.ValidateServiceAuthKey("s-auth", "service-key-v1")
	if err != nil {
		t.Fatalf("validate service key: %v", err)
	}
	if !svcOK {
		t.Fatalf("expected service key to validate")
	}

	svcOK, err = db.ValidateServiceAuthKey("s-auth", "bad-service-key")
	if err != nil {
		t.Fatalf("validate wrong service key: %v", err)
	}
	if svcOK {
		t.Fatalf("expected wrong service key to fail")
	}
}
