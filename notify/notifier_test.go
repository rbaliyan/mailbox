package notify

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pmem "github.com/rbaliyan/mailbox/presence/memory"
)

// testStore is a minimal in-memory Store for unit tests within the notify package.
type testStore struct {
	mu      sync.RWMutex
	events  map[string][]Event
	counter int
}

func newTestStore() *testStore {
	return &testStore{events: make(map[string][]Event)}
}

func (s *testStore) Save(_ context.Context, evt *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	evt.ID = fmt.Sprintf("%d", s.counter)
	s.events[evt.UserID] = append(s.events[evt.UserID], *evt)
	return nil
}

func (s *testStore) List(_ context.Context, userID string, afterID string, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}
	events := s.events[userID]
	start := 0
	if afterID != "" {
		for i, e := range events {
			if e.ID == afterID {
				start = i + 1
				break
			}
		}
	}
	if start >= len(events) {
		return nil, nil
	}
	end := min(start+limit, len(events))
	result := make([]Event, end-start)
	copy(result, events[start:end])
	return result, nil
}

func (s *testStore) Cleanup(_ context.Context, _ time.Time) error { return nil }
func (s *testStore) Close(_ context.Context) error                { return nil }

func TestPushAndSubscribeLocal(t *testing.T) {
	store := newTestStore()
	n := NewNotifier(WithStore(store))
	ctx := context.Background()

	stream, err := n.Subscribe(ctx, "user1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// Push an event.
	err = n.Push(ctx, "user1", Event{
		Type:      "test",
		Payload:   []byte(`{"msg":"hello"}`),
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read from stream.
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	evt, err := stream.Next(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != "test" {
		t.Fatalf("expected type test, got %s", evt.Type)
	}
	if evt.UserID != "user1" {
		t.Fatalf("expected user1, got %s", evt.UserID)
	}
}

func TestPushSkipsOfflineUser(t *testing.T) {
	store := newTestStore()
	tracker := pmem.New()
	n := NewNotifier(WithStore(store), WithPresence(tracker))
	ctx := context.Background()

	// User is offline — push should be skipped.
	err := n.Push(ctx, "offline-user", Event{
		Type:      "test",
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify nothing was saved.
	events, _ := store.List(ctx, "offline-user", "", 0)
	if len(events) != 0 {
		t.Fatalf("expected 0 events for offline user, got %d", len(events))
	}
}

func TestPushSavesForOnlineUser(t *testing.T) {
	store := newTestStore()
	tracker := pmem.New()
	n := NewNotifier(WithStore(store), WithPresence(tracker))
	ctx := context.Background()

	reg, _ := tracker.Register(ctx, "user1")
	defer reg.Unregister(ctx)

	err := n.Push(ctx, "user1", Event{
		Type:      "test",
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	events, _ := store.List(ctx, "user1", "", 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestSubscribeBackfill(t *testing.T) {
	store := newTestStore()
	n := NewNotifier(WithStore(store))
	ctx := context.Background()

	// Save events directly to the store (simulating events from another instance).
	for range 3 {
		_ = store.Save(ctx, &Event{
			Type:      "backfill",
			UserID:    "user1",
			Timestamp: time.Now(),
		})
	}

	events, _ := store.List(ctx, "user1", "", 0)
	firstID := events[0].ID

	// Subscribe after the first event.
	stream, err := n.Subscribe(ctx, "user1", firstID)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// Should receive 2 backfilled events.
	for range 2 {
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		evt, err := stream.Next(readCtx)
		cancel()
		if err != nil {
			t.Fatalf("expected backfill event, got error: %v", err)
		}
		if evt.Type != "backfill" {
			t.Fatalf("expected backfill, got %s", evt.Type)
		}
	}
}

func TestStreamCloseReturnsError(t *testing.T) {
	n := NewNotifier()
	ctx := context.Background()

	stream, err := n.Subscribe(ctx, "user1", "")
	if err != nil {
		t.Fatal(err)
	}

	_ = stream.Close()

	readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err = stream.Next(readCtx)
	if err != ErrStreamClosed {
		t.Fatalf("expected ErrStreamClosed, got %v", err)
	}
}

func TestNotifierClose(t *testing.T) {
	n := NewNotifier()
	ctx := context.Background()

	stream, _ := n.Subscribe(ctx, "user1", "")

	_ = n.Close(ctx)

	readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err := stream.Next(readCtx)
	if err != ErrStreamClosed {
		t.Fatalf("expected ErrStreamClosed, got %v", err)
	}

	// Push after close should fail.
	err = n.Push(ctx, "user1", Event{})
	if err != ErrNotifierClosed {
		t.Fatalf("expected ErrNotifierClosed, got %v", err)
	}
}

func TestMultipleStreamsPerUser(t *testing.T) {
	n := NewNotifier()
	ctx := context.Background()

	s1, _ := n.Subscribe(ctx, "user1", "")
	s2, _ := n.Subscribe(ctx, "user1", "")
	defer s1.Close()
	defer s2.Close()

	_ = n.Push(ctx, "user1", Event{Type: "test", Timestamp: time.Now()})

	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	evt1, err := s1.Next(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	evt2, err := s2.Next(readCtx)
	if err != nil {
		t.Fatal(err)
	}

	if evt1.Type != "test" || evt2.Type != "test" {
		t.Fatal("expected both streams to receive the event")
	}
}

func TestConcurrentPushAndSubscribe(t *testing.T) {
	store := newTestStore()
	n := NewNotifier(WithStore(store))
	ctx := context.Background()

	const numStreams = 10
	const numEvents = 50

	streams := make([]Stream, numStreams)
	for i := range numStreams {
		s, err := n.Subscribe(ctx, "user1", "")
		if err != nil {
			t.Fatal(err)
		}
		streams[i] = s
	}

	// Push events concurrently.
	var pushWg sync.WaitGroup
	pushWg.Add(numEvents)
	for range numEvents {
		go func() {
			defer pushWg.Done()
			_ = n.Push(ctx, "user1", Event{
				Type:      "concurrent",
				Timestamp: time.Now(),
			})
		}()
	}

	// Read events concurrently from all streams.
	var readWg sync.WaitGroup
	readWg.Add(numStreams)
	for i := range numStreams {
		go func(s Stream) {
			defer readWg.Done()
			received := 0
			for received < numEvents {
				readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				_, err := s.Next(readCtx)
				cancel()
				if err != nil {
					break
				}
				received++
			}
		}(streams[i])
	}

	pushWg.Wait()
	readWg.Wait()

	for _, s := range streams {
		_ = s.Close()
	}
	_ = n.Close(ctx)
}

func TestStreamNextRespectsCallerContext(t *testing.T) {
	n := NewNotifier()
	ctx := context.Background()

	stream, _ := n.Subscribe(ctx, "user1", "")
	defer stream.Close()

	readCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err := stream.Next(readCtx)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}
