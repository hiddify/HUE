package eventstore

import (
	"fmt"

	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
)

// EventStore defines the interface for event storage
type EventStore interface {
	Store(event *domain.Event) error
	GetEvents(eventType *domain.EventType, userID *string, limit int) ([]*domain.Event, error)
	GetAllEvents(limit int) ([]*domain.Event, error)
	Close() error
}

// StoreType represents the type of event store
type StoreType string

const (
	StoreTypeDB   StoreType = "db"
	StoreTypeFile StoreType = "file"
	StoreTypeNone StoreType = "none"
)

// New creates a new EventStore based on the configured type
func New(storeType string, historyDB *sqlite.HistoryDB) (EventStore, error) {
	switch StoreType(storeType) {
	case StoreTypeDB:
		return NewDBEventStore(historyDB), nil
	case StoreTypeFile:
		return nil, fmt.Errorf("file-based event store not yet implemented")
	case StoreTypeNone:
		return NewNullEventStore(), nil
	default:
		return NewDBEventStore(historyDB), nil
	}
}

// DBEventStore stores events in the database
type DBEventStore struct {
	db *sqlite.HistoryDB
}

// NewDBEventStore creates a new database-backed event store
func NewDBEventStore(db *sqlite.HistoryDB) *DBEventStore {
	return &DBEventStore{db: db}
}

// Store stores an event in the database
func (s *DBEventStore) Store(event *domain.Event) error {
	return s.db.StoreEvent(event)
}

// GetEvents retrieves events by type and user
func (s *DBEventStore) GetEvents(eventType *domain.EventType, userID *string, limit int) ([]*domain.Event, error) {
	// Use a default time range (last 30 days)
	return s.db.GetEvents(eventType, userID, nil, nil, limit)
}

// GetAllEvents retrieves all events
func (s *DBEventStore) GetAllEvents(limit int) ([]*domain.Event, error) {
	return s.db.GetEvents(nil, nil, nil, nil, limit)
}

// Close closes the event store
func (s *DBEventStore) Close() error {
	return nil // DB is managed separately
}

// NullEventStore is a no-op event store
type NullEventStore struct{}

// NewNullEventStore creates a new null event store
func NewNullEventStore() *NullEventStore {
	return &NullEventStore{}
}

// Store does nothing
func (s *NullEventStore) Store(event *domain.Event) error {
	return nil
}

// GetEvents returns empty slice
func (s *NullEventStore) GetEvents(eventType *domain.EventType, userID *string, limit int) ([]*domain.Event, error) {
	return []*domain.Event{}, nil
}

// GetAllEvents returns empty slice
func (s *NullEventStore) GetAllEvents(limit int) ([]*domain.Event, error) {
	return []*domain.Event{}, nil
}

// Close does nothing
func (s *NullEventStore) Close() error {
	return nil
}
