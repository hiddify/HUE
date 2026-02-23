package cache

import (
	"testing"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
)

func TestMemoryCacheUserSessionPenaltyAndDisconnectFlow(t *testing.T) {
	c := NewMemoryCache()

	pkgID := "pkg-1"
	c.SetUser("u1", domain.UserStatusActive, &pkgID, 2)
	c.UpdateUserUsage("u1", 10, 20)

	u := c.GetUser("u1")
	if u == nil || u.CurrentTotal != 30 || u.MaxConcurrent != 2 {
		t.Fatalf("unexpected user cache entry")
	}

	sc := c.GetOrCreateSessionCache("u1")
	sc.AddSession("s1", "hash1", "US", "NY", "ISP")
	if !sc.HasSession("s1") {
		t.Fatalf("expected session to exist")
	}
	if sc.GetActiveSessionCount(time.Minute) != 1 {
		t.Fatalf("expected one active session")
	}

	c.SetPenalty("u1", "reason", 20*time.Millisecond)
	if c.GetPenalty("u1") == nil {
		t.Fatalf("expected active penalty")
	}
	time.Sleep(30 * time.Millisecond)
	if c.GetPenalty("u1") != nil {
		t.Fatalf("expected penalty to expire")
	}

	c.QueueDisconnect("u1", "s1", "test", "n1")
	batch := c.GetDisconnectBatch()
	if len(batch) != 1 || batch[0].UserID != "u1" {
		t.Fatalf("unexpected disconnect batch")
	}
	if len(c.GetDisconnectBatch()) != 0 {
		t.Fatalf("expected disconnect queue to be cleared")
	}

	c.SetNode("n1", 2.0)
	c.UpdateNodeUsage("n1", 5, 7)
	n := c.GetNode("n1")
	if n == nil || n.CurrentUpload != 5 || n.CurrentDownload != 7 {
		t.Fatalf("unexpected node usage in cache")
	}
}
