package mailbox

import (
	"context"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

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
