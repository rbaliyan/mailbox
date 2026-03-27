package memory

import (
	"context"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/notify"
)

func TestSaveAndList(t *testing.T) {
	s := New()
	ctx := context.Background()

	evt := &notify.Event{
		Type:      "test.event",
		UserID:    "user1",
		Payload:   []byte(`{"key":"value"}`),
		Timestamp: time.Now(),
	}
	if err := s.Save(ctx, evt); err != nil {
		t.Fatal(err)
	}
	if evt.ID == "" {
		t.Fatal("expected ID to be assigned")
	}

	events, err := s.List(ctx, "user1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "test.event" {
		t.Fatalf("expected test.event, got %s", events[0].Type)
	}
}

func TestListAfterID(t *testing.T) {
	s := New()
	ctx := context.Background()

	var ids []string
	for i := range 5 {
		evt := &notify.Event{
			Type:      "test",
			UserID:    "user1",
			Payload:   []byte(`{}`),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		}
		_ = s.Save(ctx, evt)
		ids = append(ids, evt.ID)
	}

	// List after the 3rd event.
	events, err := s.List(ctx, "user1", ids[2], 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events after ID %s, got %d", ids[2], len(events))
	}
	if events[0].ID != ids[3] {
		t.Fatalf("expected first event ID %s, got %s", ids[3], events[0].ID)
	}
}

func TestListLimit(t *testing.T) {
	s := New()
	ctx := context.Background()

	for range 10 {
		_ = s.Save(ctx, &notify.Event{UserID: "user1", Timestamp: time.Now()})
	}

	events, _ := s.List(ctx, "user1", "", 3)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestListEmptyUser(t *testing.T) {
	s := New()
	ctx := context.Background()

	events, err := s.List(ctx, "nobody", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestCleanup(t *testing.T) {
	s := New()
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now()

	_ = s.Save(ctx, &notify.Event{UserID: "user1", Timestamp: old})
	_ = s.Save(ctx, &notify.Event{UserID: "user1", Timestamp: recent})
	_ = s.Save(ctx, &notify.Event{UserID: "user2", Timestamp: old})

	cutoff := time.Now().Add(-1 * time.Hour)
	if err := s.Cleanup(ctx, cutoff); err != nil {
		t.Fatal(err)
	}

	events, _ := s.List(ctx, "user1", "", 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event for user1 after cleanup, got %d", len(events))
	}

	events, _ = s.List(ctx, "user2", "", 0)
	if len(events) != 0 {
		t.Fatalf("expected 0 events for user2 after cleanup, got %d", len(events))
	}
}

func TestClosePreventsSave(t *testing.T) {
	s := New()
	ctx := context.Background()

	_ = s.Close(ctx)

	err := s.Save(ctx, &notify.Event{UserID: "user1"})
	if err != notify.ErrStoreClosed {
		t.Fatalf("expected ErrStoreClosed, got %v", err)
	}
}
