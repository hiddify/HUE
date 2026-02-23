package eventstore

import (
	"testing"

	"github.com/hiddify/hue-go/internal/domain"
)

func TestNewNoneStoreAndNullBehavior(t *testing.T) {
	es, err := New(string(StoreTypeNone), nil)
	if err != nil {
		t.Fatalf("new none store: %v", err)
	}

	if err := es.Store(&domain.Event{ID: "e1", Type: domain.EventUsageRecorded}); err != nil {
		t.Fatalf("null store should not error on store: %v", err)
	}

	events, err := es.GetAllEvents(10)
	if err != nil {
		t.Fatalf("null store get all events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no events from null store")
	}
}

func TestNewFileStoreReturnsNotImplemented(t *testing.T) {
	if _, err := New(string(StoreTypeFile), nil); err == nil {
		t.Fatalf("expected file store to return not implemented error")
	}
}
