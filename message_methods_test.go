package mailbox

import (
	"context"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// firstInboxMessage sends one message from alice to bob and returns bob's copy.
func firstInboxMessage(t *testing.T, svc Service) (Mailbox, Message) {
	t.Helper()
	ctx := context.Background()
	alice := svc.Client("alice")
	if _, err := alice.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	bob := svc.Client("bob")
	list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	msgs := list.All()
	if len(msgs) != 1 {
		t.Fatalf("inbox has %d messages, want 1", len(msgs))
	}
	return bob, msgs[0]
}

func newMessageTestService(t *testing.T) Service {
	t.Helper()
	svc, err := New(Config{}, WithStore(memory.New()))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

func TestMessage_MarkReadUnread(t *testing.T) {
	ctx := context.Background()
	svc := newMessageTestService(t)
	bob, msg := firstInboxMessage(t, svc)

	if msg.GetIsRead() {
		t.Fatal("new inbox message should be unread")
	}

	if err := msg.Update(ctx, MarkRead()); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if got, _ := bob.Get(ctx, msg.GetID()); !got.GetIsRead() {
		t.Error("message not marked read after Update(MarkRead())")
	}

	if err := msg.Update(ctx, MarkUnread()); err != nil {
		t.Fatalf("MarkUnread: %v", err)
	}
	if got, _ := bob.Get(ctx, msg.GetID()); got.GetIsRead() {
		t.Error("message still read after Update(MarkUnread())")
	}
}

func TestMessage_Move(t *testing.T) {
	ctx := context.Background()
	svc := newMessageTestService(t)
	bob, msg := firstInboxMessage(t, svc)

	if err := msg.Move(ctx, store.FolderArchived); err != nil {
		t.Fatalf("Move: %v", err)
	}
	got, err := bob.Get(ctx, msg.GetID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetFolderID() != store.FolderArchived {
		t.Errorf("folder = %q, want %q", got.GetFolderID(), store.FolderArchived)
	}
}

func TestMessage_DeleteRestore(t *testing.T) {
	ctx := context.Background()
	svc := newMessageTestService(t)
	bob, msg := firstInboxMessage(t, svc)

	if err := msg.Delete(ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := bob.Get(ctx, msg.GetID())
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got.GetFolderID() != store.FolderTrash {
		t.Errorf("after Delete folder = %q, want %q", got.GetFolderID(), store.FolderTrash)
	}

	if err := msg.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err = bob.Get(ctx, msg.GetID())
	if err != nil {
		t.Fatalf("get after restore: %v", err)
	}
	if got.GetFolderID() == store.FolderTrash {
		t.Error("message still in trash after Restore")
	}
}

func TestMessage_PermanentlyDelete(t *testing.T) {
	ctx := context.Background()
	svc := newMessageTestService(t)
	bob, msg := firstInboxMessage(t, svc)

	// Must be in trash before a permanent delete is allowed.
	if err := msg.Delete(ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := msg.PermanentlyDelete(ctx); err != nil {
		t.Fatalf("PermanentlyDelete: %v", err)
	}
	if _, err := bob.Get(ctx, msg.GetID()); err == nil {
		t.Error("message still retrievable after PermanentlyDelete")
	}
}

func TestMessage_AddRemoveTag(t *testing.T) {
	ctx := context.Background()
	svc := newMessageTestService(t)
	bob, msg := firstInboxMessage(t, svc)

	if err := msg.AddTag(ctx, "work"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	got, _ := bob.Get(ctx, msg.GetID())
	if !containsTag(got.GetTags(), "work") {
		t.Errorf("tags = %v, want to contain %q", got.GetTags(), "work")
	}

	if err := msg.RemoveTag(ctx, "work"); err != nil {
		t.Fatalf("RemoveTag: %v", err)
	}
	got, _ = bob.Get(ctx, msg.GetID())
	if containsTag(got.GetTags(), "work") {
		t.Errorf("tags = %v, still contains %q", got.GetTags(), "work")
	}
}

func TestMessage_Archive(t *testing.T) {
	ctx := context.Background()
	svc := newMessageTestService(t)
	bob, msg := firstInboxMessage(t, svc)

	// Archive is expressed via the Archived flag.
	if err := msg.Update(ctx, MarkArchived()); err != nil {
		t.Fatalf("Update(MarkArchived): %v", err)
	}
	got, err := bob.Get(ctx, msg.GetID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetFolderID() != store.FolderArchived {
		t.Errorf("folder = %q, want %q after archive", got.GetFolderID(), store.FolderArchived)
	}
}
