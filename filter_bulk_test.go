package mailbox

import (
	"context"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func setupFilterBulkService(t *testing.T) (Service, *memory.Store) {
	t.Helper()
	memStore := memory.New()
	svc, err := NewService(WithStore(memStore))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return svc, memStore
}

func sendN(t *testing.T, svc Service, from, to string, n int) {
	t.Helper()
	sender := svc.Client(from)
	for i := 0; i < n; i++ {
		d := mustCompose(sender)
		d.SetSubject("msg").SetBody("body").SetRecipients(to)
		if _, err := d.Send(context.Background()); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
}

func TestUpdateByFilter_MarkReadInFolder(t *testing.T) {
	ctx := context.Background()
	svc, _ := setupFilterBulkService(t)
	defer svc.Close(ctx)

	sendN(t, svc, "alice", "bob", 5)

	bob := svc.Client("bob")

	// Mark 2 as already read.
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	for i, m := range inbox.All() {
		if i < 2 {
			bob.UpdateFlags(ctx, m.GetID(), MarkRead())
		}
	}

	// Mark all unread in inbox as read.
	count, err := bob.UpdateByFilter(ctx, []store.Filter{
		store.InFolder(store.FolderInbox),
		store.IsReadFilter(false),
	}, MarkRead())
	if err != nil {
		t.Fatalf("update by filter: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 marked read, got %d", count)
	}

	// All should be read now.
	inbox, _ = bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	for _, m := range inbox.All() {
		if !m.GetIsRead() {
			t.Errorf("message %s should be read", m.GetID())
		}
	}
}

func TestMoveByFilter(t *testing.T) {
	ctx := context.Background()
	svc, _ := setupFilterBulkService(t)
	defer svc.Close(ctx)

	sendN(t, svc, "alice", "bob", 3)
	sendN(t, svc, "charlie", "bob", 2)

	bob := svc.Client("bob")

	// Move all messages from alice to archive.
	count, err := bob.MoveByFilter(ctx, []store.Filter{
		store.SenderIs("alice"),
	}, store.FolderArchived)
	if err != nil {
		t.Fatalf("move by filter: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 moved, got %d", count)
	}

	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	if len(inbox.All()) != 2 {
		t.Errorf("expected 2 in inbox, got %d", len(inbox.All()))
	}

	archived, _ := bob.Folder(ctx, store.FolderArchived, store.ListOptions{})
	if len(archived.All()) != 3 {
		t.Errorf("expected 3 in archive, got %d", len(archived.All()))
	}
}

func TestDeleteByFilter(t *testing.T) {
	ctx := context.Background()
	svc, _ := setupFilterBulkService(t)
	defer svc.Close(ctx)

	sendN(t, svc, "alice", "bob", 3)

	bob := svc.Client("bob")

	// Tag one message.
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	bob.AddTag(ctx, inbox.All()[0].GetID(), "delete-me")

	// Delete by tag.
	count, err := bob.DeleteByFilter(ctx, []store.Filter{
		store.HasTag("delete-me"),
	})
	if err != nil {
		t.Fatalf("delete by filter: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 deleted, got %d", count)
	}

	inbox, _ = bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	if len(inbox.All()) != 2 {
		t.Errorf("expected 2 in inbox, got %d", len(inbox.All()))
	}

	trash, _ := bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	if len(trash.All()) != 1 {
		t.Errorf("expected 1 in trash, got %d", len(trash.All()))
	}
}

func TestTagByFilter(t *testing.T) {
	ctx := context.Background()
	svc, _ := setupFilterBulkService(t)
	defer svc.Close(ctx)

	sendN(t, svc, "system", "bob", 3)
	sendN(t, svc, "alice", "bob", 2)

	bob := svc.Client("bob")

	// Tag all messages from system.
	count, err := bob.TagByFilter(ctx, []store.Filter{
		store.SenderIs("system"),
	}, "notification")
	if err != nil {
		t.Fatalf("tag by filter: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 tagged, got %d", count)
	}

	// Verify via filter.
	iter, _ := bob.Stream(ctx, []store.Filter{
		store.InFolder(store.FolderInbox),
		store.HasTag("notification"),
	}, StreamOptions{})
	var tagged int
	for {
		ok, _ := iter.Next(ctx)
		if !ok {
			break
		}
		tagged++
	}
	if tagged != 3 {
		t.Errorf("expected 3 with tag, got %d", tagged)
	}
}

func TestUntagByFilter(t *testing.T) {
	ctx := context.Background()
	svc, _ := setupFilterBulkService(t)
	defer svc.Close(ctx)

	sendN(t, svc, "alice", "bob", 3)

	bob := svc.Client("bob")

	// Tag all messages.
	bob.TagByFilter(ctx, []store.Filter{
		store.InFolder(store.FolderInbox),
	}, "bulk-tag")

	// Untag all.
	count, err := bob.UntagByFilter(ctx, []store.Filter{
		store.HasTag("bulk-tag"),
	}, "bulk-tag")
	if err != nil {
		t.Fatalf("untag by filter: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 untagged, got %d", count)
	}

	// Verify no messages have the tag.
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	for _, m := range inbox.All() {
		for _, tag := range m.GetTags() {
			if tag == "bulk-tag" {
				t.Errorf("message %s still has tag", m.GetID())
			}
		}
	}
}
