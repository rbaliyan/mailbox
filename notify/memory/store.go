package memory

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/notify"
)

// Compile-time interface check.
var _ notify.Store = (*Store)(nil)

// Store is an in-memory notification store for single-instance and testing.
type Store struct {
	mu      sync.RWMutex
	events  map[string][]notify.Event // userID -> events
	counter int64
	closed  int32
}

// New creates a new in-memory notification store.
func New() *Store {
	return &Store{
		events: make(map[string][]notify.Event),
	}
}

// Save persists a notification event in memory.
func (s *Store) Save(_ context.Context, evt *notify.Event) error {
	if atomic.LoadInt32(&s.closed) != 0 {
		return notify.ErrNotifierClosed
	}

	id := atomic.AddInt64(&s.counter, 1)
	evt.ID = fmt.Sprintf("%d", id)

	s.mu.Lock()
	s.events[evt.UserID] = append(s.events[evt.UserID], *evt)
	s.mu.Unlock()

	return nil
}

// List returns notifications for a user after the given event ID.
func (s *Store) List(_ context.Context, userID string, afterID string, limit int) ([]notify.Event, error) {
	if atomic.LoadInt32(&s.closed) != 0 {
		return nil, notify.ErrNotifierClosed
	}

	if limit <= 0 {
		limit = 100
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	events := s.events[userID]
	if len(events) == 0 {
		return nil, nil
	}

	// Find start position after afterID.
	start := 0
	if afterID != "" {
		for i, evt := range events {
			if evt.ID == afterID {
				start = i + 1
				break
			}
		}
	}

	if start >= len(events) {
		return nil, nil
	}

	end := start + limit
	if end > len(events) {
		end = len(events)
	}

	result := make([]notify.Event, end-start)
	copy(result, events[start:end])
	return result, nil
}

// Cleanup removes notifications older than the given time.
func (s *Store) Cleanup(_ context.Context, olderThan time.Time) error {
	if atomic.LoadInt32(&s.closed) != 0 {
		return notify.ErrNotifierClosed
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for userID, events := range s.events {
		kept := make([]notify.Event, 0, len(events))
		for _, evt := range events {
			if !evt.Timestamp.Before(olderThan) {
				kept = append(kept, evt)
			}
		}
		if len(kept) == 0 {
			delete(s.events, userID)
		} else {
			s.events[userID] = kept
		}
	}
	return nil
}

// Close marks the store as closed.
func (s *Store) Close(_ context.Context) error {
	atomic.StoreInt32(&s.closed, 1)
	return nil
}
