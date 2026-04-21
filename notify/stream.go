package notify

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// stream implements Stream with local channel delivery and store polling.
//
// Termination is signalled exclusively via context cancellation.
// The channel is never closed — this avoids send-on-closed-channel races
// between deliverLocal/pollStore and Close running concurrently.
type stream struct {
	ch           chan Event
	store        Store
	notifier     *Notifier
	userID       string
	pollInterval time.Duration
	ctx          context.Context
	cancel       context.CancelFunc
	closed       atomic.Bool
	dropped      atomic.Int64 // events dropped due to slow consumer
	wg           sync.WaitGroup // tracks the pollLoop goroutine

	mu     sync.Mutex // Protects lastID
	lastID string
}

var _ Stream = (*stream)(nil)

// Next blocks until the next event is available or ctx is cancelled.
func (s *stream) Next(ctx context.Context) (Event, error) {
	select {
	case evt := <-s.ch:
		s.setLastID(evt.ID)
		return evt, nil
	case <-s.ctx.Done():
		return Event{}, ErrStreamClosed
	case <-ctx.Done():
		return Event{}, ctx.Err()
	}
}

// Close stops the stream and removes it from the notifier.
// Context cancellation terminates Next and pollLoop; the channel is not
// closed, so concurrent senders never panic.
// Close blocks until the pollLoop goroutine has exited.
func (s *stream) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.cancel()
	s.wg.Wait() // wait for pollLoop to exit before removing from notifier
	if s.notifier != nil {
		s.notifier.removeStream(s.userID, s)
	}
	return nil
}

// Dropped returns the number of events dropped due to slow consumption.
func (s *stream) Dropped() int64 {
	return s.dropped.Load()
}

var _ DroppedCounter = (*stream)(nil)

func (s *stream) getLastID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastID
}

func (s *stream) setLastID(id string) {
	s.mu.Lock()
	s.lastID = id
	s.mu.Unlock()
}

// pollLoop periodically checks the store for events written by other instances.
func (s *stream) pollLoop() {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.pollStore()
		}
	}
}

// pollStore fetches new events from the store and pushes them to the channel.
func (s *stream) pollStore() {
	if s.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()

	events, err := s.store.List(ctx, s.userID, s.getLastID(), 100)
	if err != nil {
		return // Transient error — will retry on next tick.
	}

	for _, evt := range events {
		select {
		case s.ch <- evt:
			s.setLastID(evt.ID)
		case <-s.ctx.Done():
			return
		default:
			// Buffer full — try again next tick.
			return
		}
	}
}
