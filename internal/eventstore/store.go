// Package eventstore persists audit events and fans them out to
// in-process subscribers (consumed by the gRPC StreamEvents RPC).
//
// Subscribers receive events on buffered channels. When a subscriber's
// buffer is full, events for that subscriber are dropped — but every drop
// is logged and counted so it's never silent (REVIEW.md M2 fixed the
// legacy code's silent-drop bug).
package eventstore

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/hiddify/hue/internal/ent"
	entevent "github.com/hiddify/hue/internal/ent/event"
)

// Event is the in-memory representation of an audit event. Persisted shape
// matches internal/ent/schema/event.go.
type Event struct {
	ID        uuid.UUID
	Type      string
	UserID    string
	PlanID    string
	NodeID    string
	ServiceID string
	ManagerID string
	Tags      []string
	Metadata  map[string]any
	Timestamp time.Time
}

// Filter narrows what a subscriber wants. Empty slice = match everything.
type Filter struct {
	Types     []string
	UserID    string
	ManagerID string
}

// Store appends events to the database and pushes them to live
// subscribers. Both operations are best-effort: a DB write failure is
// returned to the caller (so the engine can decide), a subscriber drop is
// logged.
type Store struct {
	db     *ent.Client
	logger *slog.Logger

	mu          sync.RWMutex
	subscribers map[uuid.UUID]*subscription
}

type subscription struct {
	id     uuid.UUID
	ch     chan Event
	filter Filter
	drops  atomic.Uint64
}

func New(db *ent.Client, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{
		db:          db,
		logger:      logger,
		subscribers: make(map[uuid.UUID]*subscription),
	}
}

// Append persists the event and fans it out. Persistence is the source of
// truth — if it fails, no fan-out happens.
func (s *Store) Append(ctx context.Context, e Event) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	created, err := s.db.Event.Create().
		SetID(e.ID).
		SetType(entevent.Type(e.Type)).
		SetUserID(e.UserID).
		SetPlanID(e.PlanID).
		SetNodeID(e.NodeID).
		SetServiceID(e.ServiceID).
		SetManagerID(e.ManagerID).
		SetTags(e.Tags).
		SetMetadata(e.Metadata).
		SetTs(e.Timestamp).
		Save(ctx)
	if err != nil {
		return err
	}
	e.ID = created.ID
	s.fanout(e)
	return nil
}

// Subscribe registers a new subscriber and returns a receive-only channel
// plus an unsubscribe function. Buffer is the maximum number of pending
// events the subscriber can buffer before drops begin.
func (s *Store) Subscribe(filter Filter, buffer int) (uuid.UUID, <-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	sub := &subscription{
		id:     uuid.New(),
		ch:     make(chan Event, buffer),
		filter: filter,
	}
	s.mu.Lock()
	s.subscribers[sub.id] = sub
	s.mu.Unlock()
	return sub.id, sub.ch, func() { s.unsubscribe(sub.id) }
}

func (s *Store) unsubscribe(id uuid.UUID) {
	s.mu.Lock()
	sub, ok := s.subscribers[id]
	delete(s.subscribers, id)
	s.mu.Unlock()
	if ok {
		close(sub.ch)
	}
}

func (s *Store) fanout(e Event) {
	s.mu.RLock()
	subs := make([]*subscription, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		if matches(sub.filter, e) {
			subs = append(subs, sub)
		}
	}
	s.mu.RUnlock()

	for _, sub := range subs {
		select {
		case sub.ch <- e:
		default:
			n := sub.drops.Add(1)
			s.logger.Warn("event dropped: subscriber buffer full",
				"subscriber_id", sub.id,
				"event_id", e.ID,
				"event_type", e.Type,
				"total_drops", n,
			)
		}
	}
}

func matches(f Filter, e Event) bool {
	if len(f.Types) > 0 {
		found := false
		for _, t := range f.Types {
			if t == e.Type {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.UserID != "" && f.UserID != e.UserID {
		return false
	}
	if f.ManagerID != "" && f.ManagerID != e.ManagerID {
		return false
	}
	return true
}

// Drops returns the cumulative drop count for a subscriber; returns
// errors.New("not found") if the subscriber is gone.
func (s *Store) Drops(id uuid.UUID) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.subscribers[id]
	if !ok {
		return 0, errors.New("subscriber not found")
	}
	return sub.drops.Load(), nil
}
