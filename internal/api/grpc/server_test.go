package grpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/engine"
	"github.com/hiddify/hue-go/internal/eventstore"
	"github.com/hiddify/hue-go/internal/storage/cache"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	pb "github.com/hiddify/hue-go/pkg/proto"
	"go.uber.org/zap"
)

type grpcEventStore struct {
	events []*domain.Event
}

func (s *grpcEventStore) Store(event *domain.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *grpcEventStore) GetEvents(eventType *domain.EventType, userID *string, limit int) ([]*domain.Event, error) {
	out := make([]*domain.Event, 0, len(s.events))
	for _, e := range s.events {
		if eventType != nil && e.Type != *eventType {
			continue
		}
		if userID != nil && (e.UserID == nil || *e.UserID != *userID) {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *grpcEventStore) GetAllEvents(limit int) ([]*domain.Event, error) {
	if limit <= 0 || limit >= len(s.events) {
		return s.events, nil
	}
	return s.events[:limit], nil
}

func (s *grpcEventStore) Close() error { return nil }

var _ eventstore.EventStore = (*grpcEventStore)(nil)

type grpcFixture struct {
	server    *Server
	userDB    *sqlite.UserDB
	cache     *cache.MemoryCache
	userID    string
	packageID string
	nodeID    string
	serviceID string
	events    *grpcEventStore
}

func newGRPCFixture(t *testing.T) *grpcFixture {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "grpc-api.db")
	userDB, err := sqlite.NewUserDB("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("new user db: %v", err)
	}
	t.Cleanup(func() { _ = userDB.Close() })

	if err := userDB.Migrate(); err != nil {
		t.Fatalf("migrate user db: %v", err)
	}

	memoryCache := cache.NewMemoryCache()
	logger := zap.NewNop()
	quota := engine.NewQuotaEngine(userDB, nil, memoryCache, logger)
	session := engine.NewSessionManager(memoryCache, 2*time.Second, logger)
	penalty := engine.NewPenaltyHandler(memoryCache, 80*time.Millisecond, logger)
	events := &grpcEventStore{}

	s := NewServer(quota, session, penalty, nil, events, logger, "secret")
	s.SetUserDB(userDB)

	return &grpcFixture{server: s, userDB: userDB, cache: memoryCache, events: events}
}

func TestGRPCAdminCRUDAndNodeService(t *testing.T) {
	fx := newGRPCFixture(t)
	ctx := context.Background()

	createdUser, err := fx.server.CreateUser(ctx, &pb.CreateUserRequest{
		Username: "grpc-user",
		Password: "grpc-pass",
		Groups:   []string{"basic"},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	fx.userID = createdUser.Id

	listResp, err := fx.server.ListUsers(ctx, &pb.ListUsersRequest{Limit: 20})
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if listResp.Total < 1 {
		t.Fatalf("expected at least 1 user, got %d", listResp.Total)
	}

	updatedUser, err := fx.server.UpdateUser(ctx, &pb.UpdateUserRequest{
		Id:       fx.userID,
		Username: "grpc-user-updated",
		Status:   string(domain.UserStatusActive),
	})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if updatedUser.Username != "grpc-user-updated" {
		t.Fatalf("unexpected username after update: %s", updatedUser.Username)
	}

	createdNode, err := fx.server.CreateNode(ctx, &pb.CreateNodeRequest{
		Name:              "node-grpc",
		SecretKey:         "node-secret",
		AllowedIps:        []string{"10.0.0.0/8"},
		TrafficMultiplier: 1,
		ResetMode:         string(domain.ResetModeNoReset),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	fx.nodeID = createdNode.Id

	authFail, err := fx.server.Authenticate(ctx, &pb.AuthenticateRequest{SecretKey: "bad-secret"})
	if err != nil {
		t.Fatalf("authenticate with bad secret: %v", err)
	}
	if authFail.Success {
		t.Fatalf("expected bad authentication to fail")
	}

	authOK, err := fx.server.Authenticate(ctx, &pb.AuthenticateRequest{SecretKey: "node-secret"})
	if err != nil {
		t.Fatalf("authenticate with valid secret: %v", err)
	}
	if !authOK.Success || authOK.NodeId != fx.nodeID {
		t.Fatalf("expected successful auth for node %s", fx.nodeID)
	}

	createdService, err := fx.server.CreateService(ctx, &pb.CreateServiceRequest{
		NodeId:             fx.nodeID,
		SecretKey:          "svc-secret",
		Name:               "svc-grpc",
		Protocol:           "vless",
		AllowedAuthMethods: []string{"uuid", "password"},
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	fx.serviceID = createdService.Id

	createdPackage, err := fx.server.CreatePackage(ctx, &pb.CreatePackageRequest{
		UserId:        fx.userID,
		TotalTraffic:  10_000,
		ResetMode:     string(domain.ResetModeNoReset),
		Duration:      3600,
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("create package: %v", err)
	}
	fx.packageID = createdPackage.Id

	if _, err := fx.userDB.Exec(`UPDATE users SET active_package_id = ? WHERE id = ?`, fx.packageID, fx.userID); err != nil {
		t.Fatalf("attach active package: %v", err)
	}

	gotPackageByUser, err := fx.server.GetPackageByUser(ctx, &pb.GetPackageByUserRequest{UserId: fx.userID})
	if err != nil {
		t.Fatalf("get package by user: %v", err)
	}
	if gotPackageByUser.Id != fx.packageID {
		t.Fatalf("expected package %s, got %s", fx.packageID, gotPackageByUser.Id)
	}

	if _, err := fx.server.Heartbeat(ctx, &pb.HeartbeatRequest{NodeId: fx.nodeID}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	if _, err := fx.server.DeleteService(ctx, &pb.DeleteServiceRequest{Id: fx.serviceID}); err != nil {
		t.Fatalf("delete service: %v", err)
	}
	if _, err := fx.server.DeleteNode(ctx, &pb.DeleteNodeRequest{Id: fx.nodeID}); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if _, err := fx.server.DeleteUser(ctx, &pb.DeleteUserRequest{Id: fx.userID}); err != nil {
		t.Fatalf("delete user: %v", err)
	}
}

func TestGRPCUsageReportingAndEvents(t *testing.T) {
	fx := newGRPCFixture(t)
	ctx := context.Background()

	user, err := fx.server.CreateUser(ctx, &pb.CreateUserRequest{Username: "u1", Password: "p1"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	fx.userID = user.Id

	node, err := fx.server.CreateNode(ctx, &pb.CreateNodeRequest{Name: "n1", SecretKey: "n1", TrafficMultiplier: 1, ResetMode: string(domain.ResetModeNoReset)})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	fx.nodeID = node.Id

	service, err := fx.server.CreateService(ctx, &pb.CreateServiceRequest{NodeId: fx.nodeID, SecretKey: "s1", Name: "s1", Protocol: "vless", AllowedAuthMethods: []string{"uuid"}})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	fx.serviceID = service.Id

	pkg, err := fx.server.CreatePackage(ctx, &pb.CreatePackageRequest{UserId: fx.userID, TotalTraffic: 50, ResetMode: string(domain.ResetModeNoReset), Duration: 3600, MaxConcurrent: 1})
	if err != nil {
		t.Fatalf("create package: %v", err)
	}
	fx.packageID = pkg.Id

	if _, err := fx.userDB.Exec(`UPDATE users SET active_package_id = ? WHERE id = ?`, fx.packageID, fx.userID); err != nil {
		t.Fatalf("attach active package: %v", err)
	}

	resp1, err := fx.server.ReportUsage(ctx, &pb.ReportUsageRequest{Report: &pb.UsageReport{
		Id:        "r1",
		UserId:    fx.userID,
		NodeId:    fx.nodeID,
		ServiceId: fx.serviceID,
		Upload:    10,
		Download:  5,
		SessionId: "sess-1",
		ClientIp:  "1.1.1.1",
		Timestamp: time.Now().Unix(),
	}})
	if err != nil {
		t.Fatalf("report usage first: %v", err)
	}
	if !resp1.Result.Accepted {
		t.Fatalf("expected first usage report accepted, got reason=%s", resp1.Result.Reason)
	}

	resp2, err := fx.server.ReportUsage(ctx, &pb.ReportUsageRequest{Report: &pb.UsageReport{
		Id:        "r2",
		UserId:    fx.userID,
		NodeId:    fx.nodeID,
		ServiceId: fx.serviceID,
		Upload:    1,
		Download:  1,
		SessionId: "sess-2",
		ClientIp:  "2.2.2.2",
		Timestamp: time.Now().Unix(),
	}})
	if err != nil {
		t.Fatalf("report usage second: %v", err)
	}
	if resp2.Result.Accepted || !resp2.Result.PenaltyApplied {
		t.Fatalf("expected second report to trigger penalty")
	}

	batch, err := fx.server.BatchReportUsage(ctx, &pb.BatchReportUsageRequest{Reports: []*pb.UsageReport{
		{Id: "r3", UserId: fx.userID, NodeId: fx.nodeID, ServiceId: fx.serviceID, Upload: 1, Download: 1, SessionId: "sess-3", ClientIp: "3.3.3.3", Timestamp: time.Now().Unix()},
	}})
	if err != nil {
		t.Fatalf("batch report usage: %v", err)
	}
	if len(batch.Results) != 1 {
		t.Fatalf("expected 1 batch result, got %d", len(batch.Results))
	}

	userID := fx.userID
	fx.events.events = append(fx.events.events, &domain.Event{
		ID:      "ev-1",
		Type:    domain.EventUsageRecorded,
		UserID:  &userID,
		Tags:    []string{"grpc"},
		Timestamp: time.Now(),
	})

	gotEvents, err := fx.server.GetEvents(ctx, &pb.GetEventsRequest{Type: string(domain.EventUsageRecorded), UserId: fx.userID, Limit: 10})
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(gotEvents.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(gotEvents.Events))
	}
}
