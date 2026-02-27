package eventstore

import (
	"testing"
	"time"

	"github.com/hiddify/hue-go/internal/domain"
)

func TestReceiverHub_PublishBestEffortAndFilter(t *testing.T) {
	hub := NewReceiverHub()

	usageCh := hub.Subscribe("usage", 1, []domain.EventType{domain.EventUsageRecorded})
	allCh := hub.Subscribe("all", 2, nil)

	hub.Publish(&domain.Event{ID: "1", Type: domain.EventUsageRecorded})
	hub.Publish(&domain.Event{ID: "2", Type: domain.EventUsageRecorded})
	hub.Publish(&domain.Event{ID: "3", Type: domain.EventUserConnected})

	select {
	case ev := <-usageCh:
		if ev.Type != domain.EventUsageRecorded {
			t.Fatalf("unexpected event type for filtered receiver: %s", ev.Type)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("expected event for usage receiver")
	}

	countAll := 0
	for {
		select {
		case <-allCh:
			countAll++
		default:
			if countAll == 0 {
				t.Fatalf("expected all receiver to get at least one event")
			}
			return
		}
	}
}
