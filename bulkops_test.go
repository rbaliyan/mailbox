package mailbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/store"
)

// bulkSend sends n messages from alice to bob and returns bob's inbox message
// IDs (bob owns those copies).
func bulkSend(t *testing.T, svc mailbox.Service, n int) (bob mailbox.Mailbox, ids []string) {
	t.Helper()
	ctx := context.Background()
	alice := svc.Client("alice")
	bob = svc.Client("bob")
	for range n {
		if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "bulk",
			Body:         "body",
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
		list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 100})
		return err == nil && len(list.All()) >= n
	}, "inbox did not fill")
	list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	for _, m := range list.All() {
		ids = append(ids, m.GetID())
	}
	return bob, ids
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

func asBulkErr(err error, target **mailbox.BulkOperationError) bool {
	return errors.As(err, target)
}

// TestBulkOperations covers the four Bulk* methods, asserting BulkResult
// helpers and actual store state via re-Get.
func TestBulkOperations(t *testing.T) {
	ctx := context.Background()

	t.Run("BulkUpdateFlags marks read", func(t *testing.T) {
		svc := newTestService()
		defer svc.Close(ctx)
		bob, ids := bulkSend(t, svc, 3)

		res, err := bob.BulkUpdateFlags(ctx, ids, mailbox.MarkRead())
		if err != nil {
			t.Fatalf("bulk update flags: %v", err)
		}
		if got := res.SuccessfulIDs(); len(got) != 3 {
			t.Errorf("successful = %v, want 3", got)
		}
		if got := res.FailedIDs(); len(got) != 0 {
			t.Errorf("failed = %v, want 0", got)
		}
		// Verify state.
		for _, id := range ids {
			m, err := bob.Get(ctx, id)
			if err != nil {
				t.Fatalf("get %s: %v", id, err)
			}
			if !m.GetIsRead() {
				t.Errorf("message %s not marked read", id)
			}
		}
	})

	t.Run("BulkAddTag and BulkRemoveTag", func(t *testing.T) {
		svc := newTestService()
		defer svc.Close(ctx)
		bob, ids := bulkSend(t, svc, 2)

		addRes, err := bob.BulkAddTag(ctx, ids, "important")
		if err != nil {
			t.Fatalf("bulk add tag: %v", err)
		}
		if len(addRes.SuccessfulIDs()) != 2 {
			t.Errorf("add tag successful = %v", addRes.SuccessfulIDs())
		}
		for _, id := range ids {
			m, _ := bob.Get(ctx, id)
			if !hasTag(m.GetTags(), "important") {
				t.Errorf("message %s missing tag after add", id)
			}
		}

		rmRes, err := bob.BulkRemoveTag(ctx, ids, "important")
		if err != nil {
			t.Fatalf("bulk remove tag: %v", err)
		}
		if len(rmRes.SuccessfulIDs()) != 2 {
			t.Errorf("remove tag successful = %v", rmRes.SuccessfulIDs())
		}
		for _, id := range ids {
			m, _ := bob.Get(ctx, id)
			if hasTag(m.GetTags(), "important") {
				t.Errorf("message %s still tagged after remove", id)
			}
		}
	})

	t.Run("BulkPermanentlyDelete from trash", func(t *testing.T) {
		svc := newTestService()
		defer svc.Close(ctx)
		bob, ids := bulkSend(t, svc, 2)

		// Move to trash first (PermanentlyDelete only operates on trashed messages).
		for _, id := range ids {
			if err := bob.Delete(ctx, id); err != nil {
				t.Fatalf("soft delete %s: %v", id, err)
			}
		}

		res, err := bob.BulkPermanentlyDelete(ctx, ids)
		if err != nil {
			t.Fatalf("bulk permanently delete: %v", err)
		}
		if len(res.SuccessfulIDs()) != 2 {
			t.Errorf("successful = %v, want 2", res.SuccessfulIDs())
		}
		// Messages are gone.
		for _, id := range ids {
			if _, err := bob.Get(ctx, id); err == nil {
				t.Errorf("message %s still retrievable after permanent delete", id)
			}
		}
	})

	t.Run("partial failure surfaces in BulkResult error and Unwrap", func(t *testing.T) {
		svc := newTestService()
		defer svc.Close(ctx)
		bob, ids := bulkSend(t, svc, 1)

		// Mix a valid ID with a bogus one. AddTag on a nonexistent message fails.
		mixed := []string{ids[0], "does-not-exist"}
		res, err := bob.BulkAddTag(ctx, mixed, "tag")
		if err == nil {
			t.Fatal("expected error for partial failure")
		}
		if len(res.SuccessfulIDs()) != 1 || res.SuccessfulIDs()[0] != ids[0] {
			t.Errorf("successful = %v, want [%s]", res.SuccessfulIDs(), ids[0])
		}
		if got := res.FailedIDs(); len(got) != 1 || got[0] != "does-not-exist" {
			t.Errorf("failed = %v, want [does-not-exist]", got)
		}

		// The error unwraps to the individual operation errors.
		var boe *mailbox.BulkOperationError
		if !asBulkErr(err, &boe) {
			t.Fatalf("error is not *BulkOperationError: %T", err)
		}
		if len(boe.Unwrap()) != 1 {
			t.Errorf("Unwrap returned %d errors, want 1", len(boe.Unwrap()))
		}
	})
}
