package mailbox

import (
	"context"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func TestOutboxEventsContextRoundTrip(t *testing.T) {
	ctx := context.Background()

	// Empty context has no events.
	if events := store.OutboxEventsFromCtx(ctx); len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}

	// Add events to context.
	evt1 := store.OutboxEvent{Name: "test.event1", MessageID: "msg1", Payload: []byte(`{"a":1}`)}
	evt2 := store.OutboxEvent{Name: "test.event2", MessageID: "msg2", Payload: []byte(`{"b":2}`)}
	ctx = store.WithOutboxEvents(ctx, evt1, evt2)

	events := store.OutboxEventsFromCtx(ctx)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Name != "test.event1" || events[1].Name != "test.event2" {
		t.Errorf("events mismatch: %v", events)
	}

	// Appending more events preserves existing.
	evt3 := store.OutboxEvent{Name: "test.event3", MessageID: "msg3"}
	ctx = store.WithOutboxEvents(ctx, evt3)
	events = store.OutboxEventsFromCtx(ctx)
	if len(events) != 3 {
		t.Errorf("expected 3 events after append, got %d", len(events))
	}
}

func TestMemoryOutboxPersister(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	memStore.Connect(ctx)
	defer memStore.Close(ctx)

	// Memory store implements OutboxPersister.
	var s store.Store = memStore
	op, ok := s.(store.OutboxPersister)
	if !ok {
		t.Fatal("memory store should implement OutboxPersister")
	}

	// Outbox is always disabled for memory store.
	if op.OutboxEnabled() {
		t.Error("memory store outbox should be disabled")
	}

	// WithOutboxCtx passes through.
	called := false
	err := op.WithOutboxCtx(ctx, func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithOutboxCtx: %v", err)
	}
	if !called {
		t.Error("fn should have been called")
	}
}

func TestOpAndPublish_DirectPath(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	// Send a message — uses opAndPublish internally for all events.
	alice := svc.Client("alice")
	msg, err := alice.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "outbox test",
		Body:         "body",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.GetSubject() != "outbox test" {
		t.Errorf("expected subject 'outbox test', got %q", msg.GetSubject())
	}

	// Verify message delivered (events fired via direct path).
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 message in bob's inbox, got %d", len(inbox.All()))
	}
}

func TestMoveAndPublish_DirectPath(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "move test",
		Body:         "body",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msgID := inbox.All()[0].GetID()

	// Delete (soft) — uses moveAndPublish.
	if err := bob.Delete(ctx, msgID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	trash, _ := bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	if len(trash.All()) != 1 {
		t.Errorf("expected 1 in trash, got %d", len(trash.All()))
	}

	// Restore — uses moveAndPublish.
	if err := bob.Restore(ctx, msgID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	inbox, _ = bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 in inbox after restore, got %d", len(inbox.All()))
	}

	// Move to archive — uses moveAndPublish.
	if err := bob.MoveToFolder(ctx, msgID, store.FolderArchived); err != nil {
		t.Fatalf("move: %v", err)
	}
	archived, _ := bob.Folder(ctx, store.FolderArchived, store.ListOptions{})
	if len(archived.All()) != 1 {
		t.Errorf("expected 1 in archive, got %d", len(archived.All()))
	}
}

func TestMarkRead_ViaOpAndPublish(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "read test",
		Body:         "body",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msgID := inbox.All()[0].GetID()

	// Mark read.
	if err := bob.UpdateFlags(ctx, msgID, MarkRead()); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	msg, _ := bob.Get(ctx, msgID)
	if !msg.GetIsRead() {
		t.Error("message should be read")
	}

	// Mark unread (no event should fire, but DB write should work).
	if err := bob.UpdateFlags(ctx, msgID, MarkUnread()); err != nil {
		t.Fatalf("mark unread: %v", err)
	}
	msg, _ = bob.Get(ctx, msgID)
	if msg.GetIsRead() {
		t.Error("message should be unread")
	}
}

func TestPermanentlyDelete_ViaOpAndPublish(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "delete test",
		Body:         "body",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msgID := inbox.All()[0].GetID()

	// Soft delete first.
	bob.Delete(ctx, msgID)

	// Permanent delete — uses opAndPublish.
	if err := bob.PermanentlyDelete(ctx, msgID); err != nil {
		t.Fatalf("permanent delete: %v", err)
	}

	// Verify gone.
	trash, _ := bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	if len(trash.All()) != 0 {
		t.Errorf("expected 0 in trash, got %d", len(trash.All()))
	}
}

func TestNewOutboxEvent(t *testing.T) {
	evt, err := newOutboxEvent("test.event", "msg-123", MessageSentEvent{
		MessageID: "msg-123",
		SenderID:  "alice",
		Subject:   "hello",
	})
	if err != nil {
		t.Fatalf("newOutboxEvent: %v", err)
	}
	if evt.Name != "test.event" {
		t.Errorf("name: %q", evt.Name)
	}
	if evt.MessageID != "msg-123" {
		t.Errorf("messageID: %q", evt.MessageID)
	}
	if len(evt.Payload) == 0 {
		t.Error("payload should not be empty")
	}
}

func TestPublishTypedEvent_UnknownType(t *testing.T) {
	ctx := context.Background()
	svc, _ := NewService(WithStore(memory.New()))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	s := svc.(*service)
	err := s.publishTypedEvent(ctx, "unknown", struct{}{})
	if err == nil {
		t.Fatal("expected error for unknown event type")
	}
}

func TestPublishOutboxEvent_AllEventTypes(t *testing.T) {
	ctx := context.Background()
	svc, _ := NewService(WithStore(memory.New()))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	s := svc.(*service)

	tests := []struct {
		name    string
		evtName string
		data    any
	}{
		{
			name:    "MessageSent",
			evtName: EventNameMessageSent,
			data:    MessageSentEvent{MessageID: "m1", SenderID: "alice", Subject: "hi"},
		},
		{
			name:    "MessageReceived",
			evtName: EventNameMessageReceived,
			data:    MessageReceivedEvent{MessageID: "m2", RecipientID: "bob", SenderID: "alice"},
		},
		{
			name:    "MessageRead",
			evtName: EventNameMessageRead,
			data:    MessageReadEvent{MessageID: "m3", UserID: "bob"},
		},
		{
			name:    "MessageDeleted",
			evtName: EventNameMessageDeleted,
			data:    MessageDeletedEvent{MessageID: "m4", UserID: "bob"},
		},
		{
			name:    "MessageMoved",
			evtName: EventNameMessageMoved,
			data:    MessageMovedEvent{MessageID: "m5", UserID: "bob", FromFolderID: "__inbox", ToFolderID: "__archive"},
		},
		{
			name:    "MarkAllRead",
			evtName: EventNameMarkAllRead,
			data:    MarkAllReadEvent{UserID: "bob", FolderID: "__inbox", Count: 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create outbox event via newOutboxEvent (JSON round-trip).
			evt, err := newOutboxEvent(tt.evtName, "msg-id", tt.data)
			if err != nil {
				t.Fatalf("newOutboxEvent: %v", err)
			}

			// Publish via publishEventByName — the outbox relay path.
			if err := s.PublishOutboxEvent(ctx, evt); err != nil {
				t.Errorf("publishEventByName(%s): %v", tt.evtName, err)
			}
		})
	}
}

func TestPublishOutboxEvent_UnknownEvent(t *testing.T) {
	ctx := context.Background()
	svc, _ := NewService(WithStore(memory.New()))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	s := svc.(*service)

	evt := store.OutboxEvent{
		Name:      "unknown.event",
		MessageID: "msg-1",
		Payload:   []byte(`{}`),
	}
	err := s.PublishOutboxEvent(ctx, evt)
	if err == nil {
		t.Fatal("expected error for unknown event name")
	}
}

func TestPublishOutboxEvent_InvalidPayload(t *testing.T) {
	ctx := context.Background()
	svc, _ := NewService(WithStore(memory.New()))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	s := svc.(*service)

	evt := store.OutboxEvent{
		Name:      EventNameMessageSent,
		MessageID: "msg-1",
		Payload:   []byte(`{invalid json`),
	}
	err := s.PublishOutboxEvent(ctx, evt)
	if err == nil {
		t.Fatal("expected error for invalid JSON payload")
	}
}

func TestPersistOutboxEvents_MemoryNoOp(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	memStore.Connect(ctx)
	defer memStore.Close(ctx)

	var s store.Store = memStore
	op := s.(store.OutboxPersister)
	// Should succeed silently (no-op).
	err := op.PersistOutboxEvents(ctx, store.OutboxEvent{Name: "test", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("PersistOutboxEvents: %v", err)
	}
}
