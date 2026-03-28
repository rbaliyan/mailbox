package memory

import (
	"context"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

func setupConnectedStore(t *testing.T) *Store {
	t.Helper()
	s := New()
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return s
}

func createTestMessage(t *testing.T, s *Store, ownerID, folderID string) store.Message {
	t.Helper()
	msg, err := s.CreateMessage(context.Background(), store.MessageData{
		OwnerID:  ownerID,
		SenderID: "sender",
		Subject:  "test",
		Body:     "body",
		FolderID: folderID,
		Status:   store.MessageStatusSent,
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	return msg
}

func TestDeleteExpiredMessages_Basic(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	// Create messages in various folders.
	inbox := createTestMessage(t, s, "user1", store.FolderInbox)
	sent := createTestMessage(t, s, "user1", store.FolderSent)
	archived := createTestMessage(t, s, "user1", store.FolderArchived)
	trash := createTestMessage(t, s, "user1", store.FolderTrash)

	// Age all messages by 48 hours.
	s.AgeMessages(48 * time.Hour)

	// Create a fresh message that should survive.
	fresh := createTestMessage(t, s, "user1", store.FolderInbox)

	// Delete messages older than 24 hours.
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	deleted, err := s.DeleteExpiredMessages(ctx, cutoff)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 4 {
		t.Errorf("expected 4 deleted, got %d", deleted)
	}

	// Verify aged messages are gone.
	for _, id := range []string{inbox.GetID(), sent.GetID(), archived.GetID(), trash.GetID()} {
		if _, err := s.Get(ctx, id); err == nil {
			t.Errorf("message %s should have been deleted", id)
		}
	}

	// Verify fresh message survives.
	if _, err := s.Get(ctx, fresh.GetID()); err != nil {
		t.Errorf("fresh message should survive: %v", err)
	}
}

func TestDeleteExpiredMessages_ExcludesDrafts(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	// Create a regular message and a draft.
	msg := createTestMessage(t, s, "user1", store.FolderInbox)

	draft := s.NewDraft("user1")
	draft.SetSubject("my draft").SetBody("draft body")
	saved, err := s.SaveDraft(ctx, draft)
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}

	// Age all non-draft messages.
	s.AgeMessages(48 * time.Hour)

	// Delete with cutoff that covers the message but draft has isDraft=true.
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	deleted, err := s.DeleteExpiredMessages(ctx, cutoff)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Message should be gone.
	if _, err := s.Get(ctx, msg.GetID()); err == nil {
		t.Error("message should have been deleted")
	}

	// Draft should survive.
	if _, err := s.GetDraft(ctx, saved.GetID()); err != nil {
		t.Errorf("draft should survive: %v", err)
	}
}

func TestDeleteExpiredMessages_NothingExpired(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	createTestMessage(t, s, "user1", store.FolderInbox)
	createTestMessage(t, s, "user2", store.FolderSent)

	// Use a cutoff in the past — nothing should be expired.
	cutoff := time.Now().UTC().Add(-48 * time.Hour)
	deleted, err := s.DeleteExpiredMessages(ctx, cutoff)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", deleted)
	}
}

func TestDeleteExpiredMessages_NotConnected(t *testing.T) {
	s := New() // not connected
	_, err := s.DeleteExpiredMessages(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected error for not connected store")
	}
}

func TestDeleteExpiredMessages_AgeByID(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	old := createTestMessage(t, s, "user1", store.FolderInbox)
	young := createTestMessage(t, s, "user1", store.FolderInbox)

	// Only age the first message.
	s.AgeMessagesByID(48*time.Hour, old.GetID())

	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	deleted, err := s.DeleteExpiredMessages(ctx, cutoff)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	if _, err := s.Get(ctx, old.GetID()); err == nil {
		t.Error("old message should have been deleted")
	}
	if _, err := s.Get(ctx, young.GetID()); err != nil {
		t.Errorf("young message should survive: %v", err)
	}
}

func TestDeleteExpiredMessages_MultipleUsers(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	// Create messages for different users in different folders.
	createTestMessage(t, s, "alice", store.FolderInbox)
	createTestMessage(t, s, "bob", store.FolderSent)
	createTestMessage(t, s, "charlie", store.FolderArchived)

	s.AgeMessages(48 * time.Hour)

	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	deleted, err := s.DeleteExpiredMessages(ctx, cutoff)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 3 {
		t.Errorf("expected 3 deleted, got %d", deleted)
	}
}

func TestDeleteMessagesByIDs_Basic(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	msg1 := createTestMessage(t, s, "user1", store.FolderInbox)
	msg2 := createTestMessage(t, s, "user1", store.FolderSent)
	msg3 := createTestMessage(t, s, "user1", store.FolderInbox)

	// Delete two of three.
	deleted, err := s.DeleteMessagesByIDs(ctx, []string{msg1.GetID(), msg2.GetID()})
	if err != nil {
		t.Fatalf("delete by IDs: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("expected 2 deleted, got %d", len(deleted))
	}

	// Verify deleted messages are gone.
	if _, err := s.Get(ctx, msg1.GetID()); err == nil {
		t.Error("msg1 should be deleted")
	}
	if _, err := s.Get(ctx, msg2.GetID()); err == nil {
		t.Error("msg2 should be deleted")
	}

	// Verify surviving message.
	if _, err := s.Get(ctx, msg3.GetID()); err != nil {
		t.Errorf("msg3 should survive: %v", err)
	}
}

func TestDeleteMessagesByIDs_NonExistentIDs(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	msg := createTestMessage(t, s, "user1", store.FolderInbox)

	// Mix real and fake IDs.
	deleted, err := s.DeleteMessagesByIDs(ctx, []string{msg.GetID(), "nonexistent-1", "nonexistent-2"})
	if err != nil {
		t.Fatalf("delete by IDs: %v", err)
	}
	if len(deleted) != 1 {
		t.Errorf("expected 1 deleted, got %d", len(deleted))
	}
	if deleted[0] != msg.GetID() {
		t.Errorf("expected deleted ID %s, got %s", msg.GetID(), deleted[0])
	}
}

func TestDeleteMessagesByIDs_Empty(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	deleted, err := s.DeleteMessagesByIDs(ctx, nil)
	if err != nil {
		t.Fatalf("delete empty: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected 0 deleted, got %d", len(deleted))
	}
}

func TestDeleteMessagesByIDs_Idempotent(t *testing.T) {
	ctx := context.Background()
	s := setupConnectedStore(t)
	defer s.Close(ctx)

	msg := createTestMessage(t, s, "user1", store.FolderInbox)

	// First call deletes.
	deleted1, err := s.DeleteMessagesByIDs(ctx, []string{msg.GetID()})
	if err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if len(deleted1) != 1 {
		t.Errorf("expected 1 deleted on first call, got %d", len(deleted1))
	}

	// Second call with same ID — already gone.
	deleted2, err := s.DeleteMessagesByIDs(ctx, []string{msg.GetID()})
	if err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if len(deleted2) != 0 {
		t.Errorf("expected 0 deleted on second call, got %d", len(deleted2))
	}
}

func TestDeleteMessagesByIDs_NotConnected(t *testing.T) {
	s := New()
	_, err := s.DeleteMessagesByIDs(context.Background(), []string{"some-id"})
	if err == nil {
		t.Fatal("expected error for not connected store")
	}
}
