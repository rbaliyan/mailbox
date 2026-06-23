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

	// pollCursor is the exclusive-start cursor for store polling. It is the
	// single source of truth for the store poller's position and is advanced
	// ONLY by the poller (in event order). Live deliveries via Next must not
	// touch it: a higher-ID live event could otherwise advance the cursor past
	// an older, not-yet-polled store event, skipping it forever.
	pollMu     sync.Mutex
	pollCursor string
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

func (s *stream) setLastID(id string) {
	s.mu.Lock()
	s.lastID = id
	s.mu.Unlock()
}

func (s *stream) getPollCursor() string {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	return s.pollCursor
}

func (s *stream) setPollCursor(id string) {
	s.pollMu.Lock()
	s.pollCursor = id
	s.pollMu.Unlock()
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

	events, err := s.store.List(ctx, s.userID, s.getPollCursor(), 100)
	if err != nil {
		return // Transient error — will retry on next tick.
	}

	for _, evt := range events {
		select {
		case s.ch <- evt:
			// Advance the poll cursor in event order. Also update lastID so
			// getLastID reflects the most recently delivered event.
			s.setPollCursor(evt.ID)
			s.setLastID(evt.ID)
		case <-s.ctx.Done():
			return
		default:
			// Buffer full — try again next tick. Do not advance the cursor so
			// this event is re-fetched next time.
			return
		}
	}
}
