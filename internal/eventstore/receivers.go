package eventstore

import (
	"sync"

	"github.com/hiddify/hue-go/internal/domain"
)

type EventReceiver struct {
	ID      string
	Types   map[domain.EventType]struct{}
	Channel chan *domain.Event
}

func (r *EventReceiver) accepts(t domain.EventType) bool {
	if len(r.Types) == 0 {
		return true
	}
	_, ok := r.Types[t]
	return ok
}

type ReceiverHub struct {
	mu        sync.RWMutex
	receivers map[string]*EventReceiver
}

func NewReceiverHub() *ReceiverHub {
	return &ReceiverHub{receivers: map[string]*EventReceiver{}}
}

func (h *ReceiverHub) Subscribe(id string, bufferSize int, eventTypes []domain.EventType) <-chan *domain.Event {
	if bufferSize <= 0 {
		bufferSize = 1
	}
	types := make(map[domain.EventType]struct{}, len(eventTypes))
	for _, t := range eventTypes {
		types[t] = struct{}{}
	}

	r := &EventReceiver{
		ID:      id,
		Types:   types,
		Channel: make(chan *domain.Event, bufferSize),
	}

	h.mu.Lock()
	h.receivers[id] = r
	h.mu.Unlock()

	return r.Channel
}

func (h *ReceiverHub) Unsubscribe(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.receivers[id]
	if !ok {
		return
	}
	delete(h.receivers, id)
	close(r.Channel)
}

func (h *ReceiverHub) Publish(event *domain.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, r := range h.receivers {
		if !r.accepts(event.Type) {
			continue
		}
		select {
		case r.Channel <- event:
		default:
		}
	}
}
