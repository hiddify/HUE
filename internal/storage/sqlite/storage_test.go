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
