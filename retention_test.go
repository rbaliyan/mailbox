package mailbox

import (
	"context"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func setupRetentionService(t *testing.T, memStore *memory.Store, cfg Config, opts ...Option) Service {
	t.Helper()
	allOpts := append([]Option{WithStore(memStore)}, opts...)
	svc, err := New(cfg, allOpts...)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return svc
}

func TestCleanupExpiredMessages_Disabled(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupRetentionService(t, memStore, Config{})
	defer svc.Close(ctx)

	// Send a message so there's data.
	sender := svc.Client("alice")
	draft := mustCompose(sender)
	draft.SetSubject("hello").SetBody("body").SetRecipients("bob")
	if _, err := draft.Send(ctx); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Without MessageRetention in Config, cleanup should be a no-op.
	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.DeletedCount != 0 {
		t.Errorf("expected 0 deleted, got %d", result.DeletedCount)
	}
	if result.Interrupted {
		t.Error("should not be interrupted")
	}

	// Verify messages still exist.
	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 message in inbox, got %d", len(inbox.All()))
	}
}

func TestCleanupExpiredMessages_DeletesOldMessages(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupRetentionService(t, memStore, Config{
		MessageRetention: 24 * time.Hour,
	})
	defer svc.Close(ctx)

	// Send messages — creates copies in sender's sent and recipient's inbox.
	sender := svc.Client("alice")
	for i := range 3 {
		draft := mustCompose(sender)
		draft.SetSubject("msg").SetBody("body").SetRecipients("bob")
		if _, err := draft.Send(ctx); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	// Age all messages by 48 hours (past the 24h retention).
	memStore.AgeMessages(48 * time.Hour)

	// Send a fresh message that should survive.
	draft := mustCompose(sender)
	draft.SetSubject("fresh").SetBody("body").SetRecipients("bob")
	if _, err := draft.Send(ctx); err != nil {
		t.Fatalf("send fresh: %v", err)
	}

	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// 3 old messages * 2 copies (sender + recipient) = 6 deleted.
	if result.DeletedCount != 6 {
		t.Errorf("expected 6 deleted, got %d", result.DeletedCount)
	}

	// Fresh messages should survive.
	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 message in bob's inbox, got %d", len(inbox.All()))
	}

	sent, err := svc.Client("alice").Folder(ctx, store.FolderSent, store.ListOptions{})
	if err != nil {
		t.Fatalf("list sent: %v", err)
	}
	if len(sent.All()) != 1 {
		t.Errorf("expected 1 message in alice's sent, got %d", len(sent.All()))
	}
}

func TestCleanupExpiredMessages_PreservesDrafts(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupRetentionService(t, memStore, Config{
		MessageRetention: 24 * time.Hour,
	})
	defer svc.Close(ctx)

	// Create a draft.
	alice := svc.Client("alice")
	draft := mustCompose(alice)
	draft.SetSubject("my draft").SetBody("draft body").SetRecipients("bob")
	if _, err := draft.Save(ctx); err != nil {
		t.Fatalf("save draft: %v", err)
	}

	// Also send a real message.
	draft2 := mustCompose(alice)
	draft2.SetSubject("sent msg").SetBody("body").SetRecipients("bob")
	if _, err := draft2.Send(ctx); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Age all non-draft messages (AgeMessages skips drafts).
	memStore.AgeMessages(48 * time.Hour)

	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// 2 sent copies deleted, draft preserved.
	if result.DeletedCount != 2 {
		t.Errorf("expected 2 deleted, got %d", result.DeletedCount)
	}

	// Draft should still exist.
	drafts, err := alice.Drafts(ctx, store.ListOptions{})
	if err != nil {
		t.Fatalf("list drafts: %v", err)
	}
	if len(drafts.All()) != 1 {
		t.Errorf("expected 1 draft, got %d", len(drafts.All()))
	}
}

func TestCleanupExpiredMessages_AllFolders(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupRetentionService(t, memStore, Config{
		MessageRetention: 24 * time.Hour,
	})
	defer svc.Close(ctx)

	bob := svc.Client("bob")

	// Send messages and move them to different folders.
	for _, folder := range []string{store.FolderInbox, store.FolderArchived, store.FolderSpam} {
		sender := svc.Client("sender-" + folder)
		draft := mustCompose(sender)
		draft.SetSubject("test").SetBody("body").SetRecipients("bob")
		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send for %s: %v", folder, err)
		}

		if folder != store.FolderInbox {
			// Get bob's copy and move it.
			bobInbox, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 100})
			if err != nil {
				t.Fatalf("list inbox: %v", err)
			}
			// Find the message we just sent.
			for _, m := range bobInbox.All() {
				if m.GetSubject() == msg.GetSubject() {
					if err := bob.MoveToFolder(ctx, m.GetID(), folder); err != nil {
						t.Fatalf("move to %s: %v", folder, err)
					}
					break
				}
			}
		}
	}

	// Also put one in trash.
	sender := svc.Client("sender-trash")
	draft := mustCompose(sender)
	draft.SetSubject("test").SetBody("body").SetRecipients("bob")
	if _, err := draft.Send(ctx); err != nil {
		t.Fatalf("send for trash: %v", err)
	}
	bobInbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 100})
	if msgs := bobInbox.All(); len(msgs) > 0 {
		if err := bob.Delete(ctx, msgs[0].GetID()); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}

	// Age all messages.
	memStore.AgeMessages(48 * time.Hour)

	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// 4 messages for bob + 4 sent copies = 8 total.
	if result.DeletedCount != 8 {
		t.Errorf("expected 8 deleted, got %d", result.DeletedCount)
	}
}

func TestCleanupExpiredMessages_PreservesYoungMessages(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupRetentionService(t, memStore,
		Config{MessageRetention: 7 * 24 * time.Hour},
	)
	defer svc.Close(ctx)

	sender := svc.Client("alice")
	draft := mustCompose(sender)
	draft.SetSubject("recent").SetBody("body").SetRecipients("bob")
	if _, err := draft.Send(ctx); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Age by 3 days — still within 7-day retention.
	memStore.AgeMessages(3 * 24 * time.Hour)

	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.DeletedCount != 0 {
		t.Errorf("expected 0 deleted, got %d", result.DeletedCount)
	}

	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 message, got %d", len(inbox.All()))
	}
}

func TestCleanupExpiredMessages_NotConnected(t *testing.T) {
	memStore := memory.New()
	svc, err := New(Config{}, WithStore(memStore))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	// Don't connect — should return ErrNotConnected.
	_, err = svc.CleanupExpiredMessages(context.Background())
	if err == nil {
		t.Fatal("expected error for not connected service")
	}
}

func TestCleanupExpiredMessages_MinRetentionIgnored(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	// Retention below minimum (1 day) should be silently ignored (stays at 0 = disabled).
	svc := setupRetentionService(t, memStore,
		Config{MessageRetention: 1 * time.Hour},
	)
	defer svc.Close(ctx)

	sender := svc.Client("alice")
	draft := mustCompose(sender)
	draft.SetSubject("test").SetBody("body").SetRecipients("bob")
	if _, err := draft.Send(ctx); err != nil {
		t.Fatalf("send: %v", err)
	}

	memStore.AgeMessages(48 * time.Hour)

	// Should be a no-op since retention was below minimum and thus disabled.
	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.DeletedCount != 0 {
		t.Errorf("expected 0 deleted (retention disabled), got %d", result.DeletedCount)
	}
}
