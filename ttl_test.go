package mailbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func setupTTLService(t *testing.T, memStore *memory.Store, opts ...Option) Service {
	t.Helper()
	allOpts := append([]Option{WithStore(memStore)}, opts...)
	svc, err := NewService(allOpts...)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return svc
}

func TestMessageTTL_CleanupDeletesExpired(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore)
	defer svc.Close(ctx)

	// Send a message with a 1-hour TTL.
	sender := svc.Client("alice")
	draft := mustCompose(sender)
	draft.SetSubject("expiring").SetBody("body").SetRecipients("bob").SetTTL(1 * time.Hour)
	_, err := draft.Send(ctx)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Verify messages exist in recipient's inbox.
	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Fatalf("expected 1 message in inbox, got %d", len(inbox.All()))
	}

	// Cleanup should not delete anything yet (TTL not expired).
	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.DeletedCount != 0 {
		t.Errorf("expected 0 deleted before TTL expiry, got %d", result.DeletedCount)
	}

	// Age the TTL past expiry on all messages (shift expiresAt back by 2 hours).
	memStore.AgeTTLAll(2 * time.Hour)

	// Cleanup should now delete the expired messages.
	result, err = svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// sender copy + recipient copy = 2 messages deleted.
	if result.DeletedCount != 2 {
		t.Errorf("expected 2 deleted after TTL expiry, got %d", result.DeletedCount)
	}

	// Verify messages are gone.
	inbox, err = svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 0 {
		t.Errorf("expected 0 messages in inbox after cleanup, got %d", len(inbox.All()))
	}
}

func TestMessageTTL_DefaultTTL(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore, WithDefaultTTL(1*time.Hour))
	defer svc.Close(ctx)

	// Send a message without explicit TTL; the service default should apply.
	sender := svc.Client("alice")
	draft := mustCompose(sender)
	draft.SetSubject("default-ttl").SetBody("body").SetRecipients("bob")
	_, err := draft.Send(ctx)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Verify the message has ExpiresAt set (from default TTL).
	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Fatalf("expected 1 message, got %d", len(inbox.All()))
	}

	bobMsg, err := svc.Client("bob").Get(ctx, inbox.All()[0].GetID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if bobMsg.GetExpiresAt() == nil {
		t.Fatal("expected ExpiresAt to be set from default TTL")
	}

	// The expiry should be approximately 1 hour from now.
	expiry := *bobMsg.GetExpiresAt()
	delta := time.Until(expiry)
	if delta < 50*time.Minute || delta > 70*time.Minute {
		t.Errorf("expected expiry ~1h from now, got %v", delta)
	}
}

func TestScheduledMessage_HiddenUntilAvailable(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore)
	defer svc.Close(ctx)

	// Send a message scheduled 1 hour in the future.
	future := time.Now().UTC().Add(1 * time.Hour)
	sender := svc.Client("alice")
	draft := mustCompose(sender)
	draft.SetSubject("scheduled").SetBody("body").SetRecipients("bob").SetScheduleAt(future)
	_, err := draft.Send(ctx)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// The message should NOT appear in bob's inbox yet.
	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 0 {
		t.Errorf("expected 0 messages in inbox (scheduled), got %d", len(inbox.All()))
	}

	// The message should also NOT appear in alice's sent folder.
	sent, err := svc.Client("alice").Folder(ctx, store.FolderSent, store.ListOptions{})
	if err != nil {
		t.Fatalf("list sent: %v", err)
	}
	if len(sent.All()) != 0 {
		t.Errorf("expected 0 messages in sent (scheduled), got %d", len(sent.All()))
	}
}

func TestScheduledMessage_VisibleAfterAvailable(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore)
	defer svc.Close(ctx)

	// Send a message scheduled 1 hour in the future.
	future := time.Now().UTC().Add(1 * time.Hour)
	sender := svc.Client("alice")
	draft := mustCompose(sender)
	draft.SetSubject("scheduled").SetBody("body").SetRecipients("bob").SetScheduleAt(future)
	_, err := draft.Send(ctx)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Verify hidden initially.
	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 0 {
		t.Fatalf("expected 0 messages (scheduled), got %d", len(inbox.All()))
	}

	// Age the schedule past availability (shift all availableAt back by 2 hours).
	memStore.AgeScheduleAll(2 * time.Hour)

	// Now the messages should appear.
	inbox, err = svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 message after schedule passed, got %d", len(inbox.All()))
	}

	// Also check sender's sent folder.
	sentFolder, err := svc.Client("alice").Folder(ctx, store.FolderSent, store.ListOptions{})
	if err != nil {
		t.Fatalf("list sent: %v", err)
	}
	if len(sentFolder.All()) != 1 {
		t.Errorf("expected 1 message in sent, got %d", len(sentFolder.All()))
	}
}

func TestNoTTL_NoSchedule_DefaultBehavior(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore)
	defer svc.Close(ctx)

	// Send a message without TTL or schedule.
	sender := svc.Client("alice")
	draft := mustCompose(sender)
	draft.SetSubject("normal").SetBody("body").SetRecipients("bob")
	_, err := draft.Send(ctx)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Message should be immediately visible.
	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 message, got %d", len(inbox.All()))
	}

	// Verify ExpiresAt and AvailableAt are nil.
	bobMsg, err := svc.Client("bob").Get(ctx, inbox.All()[0].GetID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if bobMsg.GetExpiresAt() != nil {
		t.Error("expected nil ExpiresAt for message without TTL")
	}
	if bobMsg.GetAvailableAt() != nil {
		t.Error("expected nil AvailableAt for message without schedule")
	}

	// Cleanup should not delete the message (no TTL, no retention configured).
	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.DeletedCount != 0 {
		t.Errorf("expected 0 deleted, got %d", result.DeletedCount)
	}

	// Message should still be there.
	inbox, err = svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 message after cleanup, got %d", len(inbox.All()))
	}
}

func TestSendRequest_TTLAndScheduleAt(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore)
	defer svc.Close(ctx)

	// Send via SendMessage with TTL and ScheduleAt.
	future := time.Now().UTC().Add(1 * time.Hour)
	sender := svc.Client("alice")
	msg, err := sender.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "direct-send",
		Body:         "body",
		TTL:          2 * time.Hour,
		ScheduleAt:   &future,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Verify TTL+schedule invariant: expires_at = available_at + TTL.
	// Use the returned message (before any aging).
	if msg.GetExpiresAt() == nil || msg.GetAvailableAt() == nil {
		t.Fatal("expected both ExpiresAt and AvailableAt to be set")
	}
	ttlDuration := msg.GetExpiresAt().Sub(*msg.GetAvailableAt())
	if ttlDuration < 1*time.Hour+55*time.Minute || ttlDuration > 2*time.Hour+5*time.Minute {
		t.Errorf("expected expires_at ~2h after available_at, got %v", ttlDuration)
	}

	// Should be hidden (scheduled in future).
	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 0 {
		t.Errorf("expected 0 messages (scheduled), got %d", len(inbox.All()))
	}

	// Age schedule past availability.
	memStore.AgeScheduleAll(2 * time.Hour)

	// Now visible.
	inbox, err = svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 message after schedule, got %d", len(inbox.All()))
	}
}

func TestTTL_MinTTLValidation(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore, WithMinTTL(1*time.Hour))
	defer svc.Close(ctx)

	sender := svc.Client("alice")
	_, err := sender.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "too short TTL",
		Body:         "body",
		TTL:          30 * time.Minute, // below 1h minimum
	})
	if err == nil {
		t.Fatal("expected error for TTL below minimum")
	}
	if !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("expected ErrInvalidTTL, got %v", err)
	}
}

func TestTTL_MaxTTLValidation(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore, WithMaxTTL(24*time.Hour))
	defer svc.Close(ctx)

	sender := svc.Client("alice")
	_, err := sender.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "too long TTL",
		Body:         "body",
		TTL:          48 * time.Hour, // above 24h maximum
	})
	if err == nil {
		t.Fatal("expected error for TTL above maximum")
	}
	if !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("expected ErrInvalidTTL, got %v", err)
	}
}

func TestSchedule_MaxDelayValidation(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc := setupTTLService(t, memStore, WithMaxScheduleDelay(7*24*time.Hour))
	defer svc.Close(ctx)

	future := time.Now().UTC().Add(30 * 24 * time.Hour) // 30 days out
	sender := svc.Client("alice")
	_, err := sender.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "too far out",
		Body:         "body",
		ScheduleAt:   &future,
	})
	if err == nil {
		t.Fatal("expected error for schedule delay above maximum")
	}
	if !errors.Is(err, ErrInvalidSchedule) {
		t.Errorf("expected ErrInvalidSchedule, got %v", err)
	}
}
