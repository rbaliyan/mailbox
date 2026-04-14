package mailbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func setupQuotaService(t *testing.T, opts ...Option) Service {
	t.Helper()
	allOpts := append([]Option{WithStore(memory.New())}, opts...)
	svc, err := New(Config{}, allOpts...)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	return svc
}

// sendTestMessage sends a message from sender to recipient and returns any error.
func sendTestMessage(t *testing.T, svc Service, from, to, subject string) (Message, error) {
	t.Helper()
	sender := svc.Client(from)
	draft := mustCompose(sender)
	draft.SetSubject(subject).
		SetBody("test body").
		SetRecipients(to)
	return draft.Send(context.Background())
}

func TestQuotaDisabled(t *testing.T) {
	ctx := context.Background()
	svc := setupQuotaService(t)
	defer svc.Close(ctx)

	// Without quota, messages should flow freely.
	for i := range 5 {
		_, err := sendTestMessage(t, svc, "sender", "recipient", "msg")
		if err != nil {
			t.Fatalf("send %d failed: %v", i, err)
		}
	}

	inbox, err := svc.Client("recipient").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox failed: %v", err)
	}
	if len(inbox.All()) != 5 {
		t.Errorf("expected 5 messages, got %d", len(inbox.All()))
	}
}

func TestQuotaRejectMode(t *testing.T) {
	ctx := context.Background()
	svc := setupQuotaService(t, WithGlobalQuota(QuotaPolicy{
		MaxMessages:  3,
		ExceedAction: QuotaActionReject,
	}))
	defer svc.Close(ctx)

	// Send 3 messages to recipient — should all succeed.
	// Each send creates 1 message for sender (sent) + 1 for recipient (inbox) = 2 per send.
	// Quota counts ALL messages for a user, so sender hits quota too.
	// Use different senders to isolate recipient quota.
	for i := range 3 {
		senderID := "sender" + string(rune('A'+i))
		_, err := sendTestMessage(t, svc, senderID, "recipient", "msg")
		if err != nil {
			t.Fatalf("send %d failed: %v", i, err)
		}
	}

	// Verify recipient has 3 messages.
	inbox, err := svc.Client("recipient").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox failed: %v", err)
	}
	if len(inbox.All()) != 3 {
		t.Errorf("expected 3 inbox messages, got %d", len(inbox.All()))
	}

	// 4th message to recipient should fail — all recipients failed delivery.
	_, err = sendTestMessage(t, svc, "senderD", "recipient", "msg4")
	if err == nil {
		t.Fatal("expected error for over-quota delivery")
	}

	// Verify recipient still has exactly 3 messages (4th was rejected).
	inbox, err = svc.Client("recipient").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox after reject: %v", err)
	}
	if len(inbox.All()) != 3 {
		t.Errorf("expected 3 inbox messages after rejection, got %d", len(inbox.All()))
	}
}

func TestQuotaRejectPartialDelivery(t *testing.T) {
	ctx := context.Background()
	svc := setupQuotaService(t, WithGlobalQuota(QuotaPolicy{
		MaxMessages:  2,
		ExceedAction: QuotaActionReject,
	}))
	defer svc.Close(ctx)

	// Fill up r1's quota with 2 messages.
	for i := range 2 {
		senderID := "s" + string(rune('A'+i))
		_, err := sendTestMessage(t, svc, senderID, "r1", "fill")
		if err != nil {
			t.Fatalf("fill send %d failed: %v", i, err)
		}
	}

	// Send to both r1 (over quota) and r2 (under quota).
	sender := svc.Client("sender")
	draft := mustCompose(sender)
	draft.SetSubject("partial").
		SetBody("test body").
		SetRecipients("r1", "r2")

	msg, err := draft.Send(ctx)

	// Should be partial delivery: r2 gets it, r1 is rejected.
	if msg == nil {
		t.Fatal("expected message even on partial delivery")
	}

	pde, ok := IsPartialDelivery(err)
	if !ok {
		t.Fatalf("expected PartialDeliveryError, got: %v", err)
	}

	// r2 should have received the message.
	found := false
	for _, id := range pde.DeliveredTo {
		if id == "r2" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected r2 in delivered list, got: %v", pde.DeliveredTo)
	}

	// r1 should be in failed recipients with quota error.
	r1Err, ok := pde.FailedRecipients["r1"]
	if !ok {
		t.Fatal("expected r1 in failed recipients")
	}
	if !errors.Is(r1Err, ErrQuotaExceeded) {
		t.Errorf("expected quota exceeded for r1, got: %v", r1Err)
	}
}

func TestQuotaDeleteOldestNeverBlocksDelivery(t *testing.T) {
	ctx := context.Background()
	svc := setupQuotaService(t, WithGlobalQuota(QuotaPolicy{
		MaxMessages:     2,
		ExceedAction:    QuotaActionDeleteOldest,
		DeleteOlderThan: 1 * time.Millisecond,
	}))
	defer svc.Close(ctx)

	// Send 5 messages — all should succeed (delete-oldest never rejects).
	for i := range 5 {
		senderID := "s" + string(rune('A'+i))
		_, err := sendTestMessage(t, svc, senderID, "recipient", "msg")
		if err != nil {
			t.Fatalf("send %d failed: %v", i, err)
		}
	}

	inbox, err := svc.Client("recipient").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox failed: %v", err)
	}
	if len(inbox.All()) != 5 {
		t.Errorf("expected 5 messages, got %d", len(inbox.All()))
	}
}

func TestEnforceQuotas(t *testing.T) {
	ctx := context.Background()
	svc := setupQuotaService(t, WithGlobalQuota(QuotaPolicy{
		MaxMessages:     2,
		ExceedAction:    QuotaActionDeleteOldest,
		DeleteOlderThan: 1 * time.Millisecond,
	}))
	defer svc.Close(ctx)

	// Send 5 messages to recipient (use different senders to avoid sender quota issues).
	for i := range 5 {
		senderID := "s" + string(rune('A'+i))
		_, err := sendTestMessage(t, svc, senderID, "recipient", "msg")
		if err != nil {
			t.Fatalf("send %d failed: %v", i, err)
		}
	}

	// Wait for messages to age past the threshold.
	time.Sleep(5 * time.Millisecond)

	// Enforce quotas.
	result, err := svc.EnforceQuotas(ctx, []string{"recipient"})
	if err != nil {
		t.Fatalf("enforce quotas failed: %v", err)
	}

	if result.UsersChecked != 1 {
		t.Errorf("expected 1 user checked, got %d", result.UsersChecked)
	}
	if result.UsersOverQuota != 1 {
		t.Errorf("expected 1 user over quota, got %d", result.UsersOverQuota)
	}
	// Should have deleted 3 messages (5 - 2 = 3 excess).
	if result.MessagesDeleted != 3 {
		t.Errorf("expected 3 messages deleted, got %d", result.MessagesDeleted)
	}

	// Verify recipient now has exactly 2 messages.
	inbox, err := svc.Client("recipient").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox failed: %v", err)
	}
	if len(inbox.All()) != 2 {
		t.Errorf("expected 2 messages after enforcement, got %d", len(inbox.All()))
	}
}

func TestEnforceQuotasSkipsRejectMode(t *testing.T) {
	ctx := context.Background()
	svc := setupQuotaService(t, WithGlobalQuota(QuotaPolicy{
		MaxMessages:  2,
		ExceedAction: QuotaActionReject,
	}))
	defer svc.Close(ctx)

	// EnforceQuotas should be a no-op for reject mode.
	result, err := svc.EnforceQuotas(ctx, []string{"user1"})
	if err != nil {
		t.Fatalf("enforce quotas failed: %v", err)
	}
	if result.MessagesDeleted != 0 {
		t.Errorf("expected 0 deleted for reject mode, got %d", result.MessagesDeleted)
	}
	if result.UsersOverQuota != 0 {
		t.Errorf("expected 0 over quota for reject mode, got %d", result.UsersOverQuota)
	}
}

func TestEnforceQuotasDisabled(t *testing.T) {
	ctx := context.Background()
	svc := setupQuotaService(t)
	defer svc.Close(ctx)

	result, err := svc.EnforceQuotas(ctx, []string{"user1"})
	if err != nil {
		t.Fatalf("enforce quotas failed: %v", err)
	}
	if result.UsersChecked != 0 {
		t.Errorf("expected 0 users checked when disabled, got %d", result.UsersChecked)
	}
}

func TestCustomQuotaProvider(t *testing.T) {
	ctx := context.Background()

	// Custom provider: user1 has quota of 2, user2 has unlimited.
	provider := &testQuotaProvider{
		policies: map[string]*QuotaPolicy{
			"user1": {MaxMessages: 2, ExceedAction: QuotaActionReject},
			"user2": nil, // unlimited
		},
	}

	svc := setupQuotaService(t, WithQuotaProvider(provider))
	defer svc.Close(ctx)

	// Fill user1 to quota.
	for i := range 2 {
		senderID := "s" + string(rune('A'+i))
		_, err := sendTestMessage(t, svc, senderID, "user1", "msg")
		if err != nil {
			t.Fatalf("send to user1 %d failed: %v", i, err)
		}
	}

	// user1 should now reject.
	_, err := sendTestMessage(t, svc, "senderC", "user1", "overflow")
	if err == nil {
		t.Fatal("expected delivery to user1 to fail")
	}

	// user2 should accept unlimited messages.
	for i := range 10 {
		senderID := "s" + string(rune('A'+i))
		_, err := sendTestMessage(t, svc, senderID, "user2", "msg")
		if err != nil {
			t.Fatalf("send to user2 %d failed: %v", i, err)
		}
	}
}

func TestQuotaExceededError(t *testing.T) {
	qErr := &QuotaExceededError{
		UserID:       "user1",
		CurrentCount: 100,
		MaxMessages:  100,
	}

	if !errors.Is(qErr, ErrQuotaExceeded) {
		t.Error("QuotaExceededError should wrap ErrQuotaExceeded")
	}

	if IsRetryableError(qErr) {
		t.Error("ErrQuotaExceeded should not be retryable")
	}
}

func TestStaticQuotaProvider(t *testing.T) {
	policy := QuotaPolicy{MaxMessages: 500}
	p := &StaticQuotaProvider{Policy: policy}

	got, err := p.GetQuota(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MaxMessages != 500 {
		t.Errorf("expected MaxMessages=500, got %d", got.MaxMessages)
	}
}

// testQuotaProvider returns per-user policies from a map.
type testQuotaProvider struct {
	policies map[string]*QuotaPolicy
}

func (p *testQuotaProvider) GetQuota(_ context.Context, userID string) (*QuotaPolicy, error) {
	return p.policies[userID], nil
}
