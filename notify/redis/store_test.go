package redis

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rbaliyan/mailbox/notify"
	"github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	store := New(client)
	return store, mr
}

func TestSaveAndList(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	// Save 3 events.
	ids := make([]string, 3)
	for i := range 3 {
		evt := &notify.Event{
			Type:      "test",
			UserID:    "user1",
			Payload:   []byte(fmt.Sprintf(`{"i":%d}`, i)),
			Timestamp: time.Now(),
		}
		if err := store.Save(ctx, evt); err != nil {
			t.Fatal(err)
		}
		ids[i] = evt.ID
		if evt.ID == "" {
			t.Fatal("expected non-empty event ID")
		}
	}

	// List all.
	events, err := store.List(ctx, "user1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// List after first.
	events, err = store.List(ctx, "user1", ids[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events after first, got %d", len(events))
	}
	if events[0].ID != ids[1] {
		t.Fatalf("expected ID %s, got %s", ids[1], events[0].ID)
	}

	// List with limit.
	events, err = store.List(ctx, "user1", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event with limit, got %d", len(events))
	}
}

func TestSavePreservesFields(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	ts := time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
	evt := &notify.Event{
		Type:      "mailbox.message.received",
		UserID:    "user42",
		Payload:   []byte(`{"messageID":"abc"}`),
		Timestamp: ts,
	}
	if err := store.Save(ctx, evt); err != nil {
		t.Fatal(err)
	}

	events, err := store.List(ctx, "user42", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	got := events[0]
	if got.Type != "mailbox.message.received" {
		t.Errorf("type: got %q, want %q", got.Type, "mailbox.message.received")
	}
	if got.UserID != "user42" {
		t.Errorf("userID: got %q, want %q", got.UserID, "user42")
	}
	if string(got.Payload) != `{"messageID":"abc"}` {
		t.Errorf("payload: got %q", got.Payload)
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("timestamp: got %v, want %v", got.Timestamp, ts)
	}
}

func TestListEmptyStream(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	events, err := store.List(ctx, "nobody", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestSubscribeLiveDelivery(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	stream, err := store.Subscribe(ctx, "user1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// Push after subscribe.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = store.Save(ctx, &notify.Event{
			Type:      "live",
			UserID:    "user1",
			Payload:   []byte(`{}`),
			Timestamp: time.Now(),
		})
	}()

	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	evt, err := stream.Next(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != "live" {
		t.Fatalf("expected type 'live', got %q", evt.Type)
	}
}

func TestSubscribeBackfill(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	// Save events before subscribe.
	ids := make([]string, 3)
	for i := range 3 {
		evt := &notify.Event{
			Type:      "backfill",
			UserID:    "user1",
			Timestamp: time.Now(),
		}
		_ = store.Save(ctx, evt)
		ids[i] = evt.ID
	}

	// Subscribe after first event.
	stream, err := store.Subscribe(ctx, "user1", ids[0])
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// Should receive 2 backfilled events.
	for i := range 2 {
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		evt, err := stream.Next(readCtx)
		cancel()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if evt.ID != ids[i+1] {
			t.Fatalf("event %d: got ID %s, want %s", i, evt.ID, ids[i+1])
		}
	}
}

func TestSubscribeBackfillThenLive(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	// Save one event.
	first := &notify.Event{Type: "old", UserID: "user1", Timestamp: time.Now()}
	_ = store.Save(ctx, first)

	second := &notify.Event{Type: "backfilled", UserID: "user1", Timestamp: time.Now()}
	_ = store.Save(ctx, second)

	// Subscribe after first — should get second as backfill.
	stream, err := store.Subscribe(ctx, "user1", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	evt, err := stream.Next(readCtx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != "backfilled" {
		t.Fatalf("expected backfilled, got %q", evt.Type)
	}

	// Now push a live event.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = store.Save(ctx, &notify.Event{
			Type: "live", UserID: "user1", Timestamp: time.Now(),
		})
	}()

	readCtx, cancel = context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	evt, err = stream.Next(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != "live" {
		t.Fatalf("expected live, got %q", evt.Type)
	}
}

func TestStreamClose(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	stream, err := store.Subscribe(ctx, "user1", "")
	if err != nil {
		t.Fatal(err)
	}

	_ = stream.Close()

	readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err = stream.Next(readCtx)
	if err != notify.ErrStreamClosed {
		t.Fatalf("expected ErrStreamClosed, got %v", err)
	}
}

func TestStreamCallerContextCancellation(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	stream, err := store.Subscribe(ctx, "user1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	readCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = stream.Next(readCtx)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestClosedStoreRejectsOperations(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	_ = store.Close(ctx)

	if err := store.Save(ctx, &notify.Event{UserID: "u"}); err != notify.ErrStoreClosed {
		t.Fatalf("Save: expected ErrStoreClosed, got %v", err)
	}
	if _, err := store.List(ctx, "u", "", 0); err != notify.ErrStoreClosed {
		t.Fatalf("List: expected ErrStoreClosed, got %v", err)
	}
	if err := store.Cleanup(ctx, time.Now()); err != notify.ErrStoreClosed {
		t.Fatalf("Cleanup: expected ErrStoreClosed, got %v", err)
	}
	if _, err := store.Subscribe(ctx, "u", ""); err != notify.ErrStoreClosed {
		t.Fatalf("Subscribe: expected ErrStoreClosed, got %v", err)
	}
}

func TestStoreCloseTerminatesStreams(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	stream, err := store.Subscribe(ctx, "user1", "")
	if err != nil {
		t.Fatal(err)
	}

	// Close the store — should cancel all active streams.
	_ = store.Close(ctx)

	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	_, err = stream.Next(readCtx)
	if err != notify.ErrStreamClosed {
		t.Fatalf("expected ErrStreamClosed after store close, got %v", err)
	}
}

func TestConcurrentSubscribeAndPush(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	const numStreams = 5
	const numEvents = 10

	streams := make([]notify.Stream, numStreams)
	for i := range numStreams {
		s, err := store.Subscribe(ctx, "user1", "")
		if err != nil {
			t.Fatal(err)
		}
		streams[i] = s
	}

	// Push events concurrently.
	var pushWg sync.WaitGroup
	pushWg.Add(numEvents)
	for i := range numEvents {
		go func() {
			defer pushWg.Done()
			_ = store.Save(ctx, &notify.Event{
				Type:      fmt.Sprintf("evt-%d", i),
				UserID:    "user1",
				Timestamp: time.Now(),
			})
		}()
	}

	// Read from all streams concurrently.
	var readWg sync.WaitGroup
	readWg.Add(numStreams)
	for i := range numStreams {
		go func(s notify.Stream) {
			defer readWg.Done()
			for range numEvents {
				readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := s.Next(readCtx)
				cancel()
				if err != nil {
					return
				}
			}
		}(streams[i])
	}

	pushWg.Wait()
	readWg.Wait()

	for _, s := range streams {
		_ = s.Close()
	}
}

func TestNotifierWithRedisStreamStore(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()

	n := notify.NewNotifier(notify.WithStore(store))

	stream, err := n.Subscribe(ctx, "user1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// Push through the notifier.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = n.Push(ctx, "user1", notify.Event{
			Type:      "via-notifier",
			Payload:   []byte(`{}`),
			Timestamp: time.Now(),
		})
	}()

	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	evt, err := stream.Next(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != "via-notifier" {
		t.Fatalf("expected 'via-notifier', got %q", evt.Type)
	}
}
