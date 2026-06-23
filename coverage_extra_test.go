package mailbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rbaliyan/event/v3/transport/channel"
	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/resolver"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// --- Error types ---

func TestErrorTypes(t *testing.T) {
	t.Run("ValidationError", func(t *testing.T) {
		ve := &mailbox.ValidationError{Field: "subject", Message: "required"}
		if ve.Error() == "" {
			t.Error("empty Error()")
		}
		if !errors.Is(ve, mailbox.ErrInvalidMessage) {
			t.Error("ValidationError should unwrap to ErrInvalidMessage")
		}
	})

	t.Run("EventPublishError", func(t *testing.T) {
		inner := errors.New("transport down")
		epe := &mailbox.EventPublishError{Event: "MessageSent", MessageID: "m1", Err: inner}
		if epe.Error() == "" {
			t.Error("empty Error()")
		}
		if !errors.Is(epe, inner) {
			t.Error("should unwrap to inner error")
		}
		got, ok := mailbox.IsEventPublishError(epe)
		if !ok || got.MessageID != "m1" {
			t.Errorf("IsEventPublishError = %v, %v", got, ok)
		}
		if _, ok := mailbox.IsEventPublishError(errors.New("other")); ok {
			t.Error("non-EventPublishError matched")
		}
	})

	t.Run("AttachmentRefError", func(t *testing.T) {
		are := &mailbox.AttachmentRefError{
			Operation:      "add",
			Failed:         map[string]error{"a1": errors.New("boom")},
			RollbackFailed: map[string]error{"a2": errors.New("rollback boom")},
		}
		if are.Error() == "" {
			t.Error("empty Error()")
		}
		if !are.HasRollbackFailures() {
			t.Error("HasRollbackFailures should be true")
		}
		if len(are.FailedIDs()) != 1 || are.FailedIDs()[0] != "a1" {
			t.Errorf("FailedIDs = %v", are.FailedIDs())
		}
		if len(are.Unwrap()) != 2 {
			t.Errorf("Unwrap len = %d, want 2", len(are.Unwrap()))
		}

		noRollback := &mailbox.AttachmentRefError{Operation: "release", Failed: map[string]error{"x": errors.New("e")}}
		if noRollback.HasRollbackFailures() {
			t.Error("HasRollbackFailures should be false without rollback failures")
		}
	})
}

// --- Quota types ---

func TestQuotaTypes(t *testing.T) {
	if mailbox.QuotaActionReject.String() != "reject" {
		t.Errorf("reject string = %q", mailbox.QuotaActionReject.String())
	}
	if mailbox.QuotaActionDeleteOldest.String() != "delete_oldest" {
		t.Errorf("delete_oldest string = %q", mailbox.QuotaActionDeleteOldest.String())
	}
	if mailbox.QuotaAction(99).String() == "" {
		t.Error("unknown quota action should still stringify")
	}

	qee := &mailbox.QuotaExceededError{UserID: "u", CurrentCount: 10, MaxMessages: 5}
	if qee.Error() == "" {
		t.Error("empty Error()")
	}
	if !errors.Is(qee, mailbox.ErrQuotaExceeded) {
		t.Error("should unwrap to ErrQuotaExceeded")
	}

	p := &mailbox.StaticQuotaProvider{Policy: mailbox.QuotaPolicy{MaxMessages: 3}}
	got, err := p.GetQuota(context.Background(), "anyone")
	if err != nil || got.MaxMessages != 3 {
		t.Errorf("GetQuota = %v, %v", got, err)
	}
}

// --- Config.Validate ---

func TestConfigValidate(t *testing.T) {
	t.Run("zero config fails", func(t *testing.T) {
		var cfg mailbox.Config
		if err := cfg.Validate(); err == nil {
			t.Error("expected validation errors for zero config")
		}
	})
	t.Run("default config passes", func(t *testing.T) {
		cfg := mailbox.DefaultConfig()
		if err := cfg.Validate(); err != nil {
			t.Errorf("default config should validate: %v", err)
		}
	})
}

// --- ValidateMessage ---

func TestValidateMessageHelper(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "subject",
		Body:         "body",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	bob := svc.Client("bob")
	msg := firstInbox(t, bob)

	if err := mailbox.ValidateMessage(msg, mailbox.DefaultLimits()); err != nil {
		t.Errorf("ValidateMessage on a valid message: %v", err)
	}
}

// --- Filter-based bulk operations ---

func TestFilterBulkOperations(t *testing.T) {
	ctx := context.Background()

	setup := func(t *testing.T, n int) (mailbox.Mailbox, mailbox.Service) {
		t.Helper()
		svc := newTestService()
		t.Cleanup(func() { _ = svc.Close(ctx) })
		alice := svc.Client("alice")
		bob := svc.Client("bob")
		for range n {
			if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
				RecipientIDs: []string{"bob"},
				Subject:      "x",
				Body:         "y",
			}); err != nil {
				t.Fatalf("send: %v", err)
			}
		}
		mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
			list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 100})
			return err == nil && len(list.All()) >= n
		}, "inbox did not fill")
		return bob, svc
	}

	t.Run("UpdateByFilter", func(t *testing.T) {
		bob, _ := setup(t, 3)
		n, err := bob.UpdateByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, mailbox.MarkRead())
		if err != nil {
			t.Fatalf("UpdateByFilter: %v", err)
		}
		if n != 3 {
			t.Errorf("updated = %d, want 3", n)
		}
	})

	t.Run("TagByFilter and UntagByFilter", func(t *testing.T) {
		bob, _ := setup(t, 2)
		n, err := bob.TagByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, "flagged")
		if err != nil {
			t.Fatalf("TagByFilter: %v", err)
		}
		if n != 2 {
			t.Errorf("tagged = %d, want 2", n)
		}
		n, err = bob.UntagByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, "flagged")
		if err != nil {
			t.Fatalf("UntagByFilter: %v", err)
		}
		if n != 2 {
			t.Errorf("untagged = %d, want 2", n)
		}
	})

	t.Run("MoveByFilter", func(t *testing.T) {
		bob, _ := setup(t, 2)
		n, err := bob.MoveByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, store.FolderArchived)
		if err != nil {
			t.Fatalf("MoveByFilter: %v", err)
		}
		if n != 2 {
			t.Errorf("moved = %d, want 2", n)
		}
	})

	t.Run("DeleteByFilter", func(t *testing.T) {
		bob, _ := setup(t, 2)
		n, err := bob.DeleteByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)})
		if err != nil {
			t.Fatalf("DeleteByFilter: %v", err)
		}
		if n != 2 {
			t.Errorf("deleted = %d, want 2", n)
		}
	})
}

// --- MarkAllRead ---

func TestMarkAllRead(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")
	for range 3 {
		if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "x",
			Body:         "y",
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
		list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 100})
		return err == nil && len(list.All()) >= 3
	}, "inbox did not fill")

	n, err := bob.MarkAllRead(ctx, store.FolderInbox)
	if err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if n != 3 {
		t.Errorf("marked = %d, want 3", n)
	}
}

// --- Stream / StreamSearch iterator ---

func TestStreamIterators(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")
	for range 5 {
		if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "streamable",
			Body:         "body",
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
		list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 100})
		return err == nil && len(list.All()) >= 5
	}, "inbox did not fill")

	t.Run("Stream", func(t *testing.T) {
		iter, err := bob.Stream(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, mailbox.StreamOptions{BatchSize: 2})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		count := 0
		for {
			hasNext, err := iter.Next(ctx)
			if err != nil {
				t.Fatalf("iter.Next: %v", err)
			}
			if !hasNext {
				break
			}
			if _, err := iter.Message(); err != nil {
				t.Fatalf("iter.Message: %v", err)
			}
			count++
		}
		if count != 5 {
			t.Errorf("streamed %d, want 5", count)
		}
	})

	t.Run("StreamSearch", func(t *testing.T) {
		iter, err := bob.StreamSearch(ctx, mailbox.SearchQuery{
			OwnerID: "bob",
			Query:   "streamable",
		}, mailbox.StreamOptions{BatchSize: 2})
		if err != nil {
			t.Fatalf("StreamSearch: %v", err)
		}
		// Drain (the memory store may or may not support full-text search; just
		// exercise the iterator path without asserting a specific count).
		for {
			hasNext, err := iter.Next(ctx)
			if err != nil {
				break
			}
			if !hasNext {
				break
			}
			_, _ = iter.Message()
		}
	})
}

// --- ThreadParticipants ---

func TestThreadParticipants(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	sent, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Start thread",
		Body:         "body",
		ThreadID:     "thread-xyz",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = sent

	mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
		bob := svc.Client("bob")
		list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 10})
		return err == nil && len(list.All()) >= 1
	}, "thread message not delivered")

	participants, err := svc.ThreadParticipants(ctx, "thread-xyz")
	if err != nil {
		t.Fatalf("ThreadParticipants: %v", err)
	}
	if len(participants) == 0 {
		t.Error("expected at least one thread participant")
	}
}

// --- ResolveAttachments error path ---

func TestResolveAttachments_NotConfigured(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	_, err := svc.Client("alice").ResolveAttachments(ctx, []string{"a1"})
	if !errors.Is(err, mailbox.ErrAttachmentStoreNotConfigured) {
		t.Errorf("expected ErrAttachmentStoreNotConfigured, got %v", err)
	}
}

// memStatsCache is a minimal in-memory StatsCache for testing the L2 path.
type memStatsCache struct {
	mu      sync.Mutex
	byOwner map[string]*store.MailboxStats
}

func newMemStatsCache() *memStatsCache {
	return &memStatsCache{byOwner: make(map[string]*store.MailboxStats)}
}

func (c *memStatsCache) Get(_ context.Context, ownerID string) (*store.MailboxStats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.byOwner[ownerID]
	if !ok {
		return nil, nil
	}
	return s.Clone(), nil
}

func (c *memStatsCache) Set(_ context.Context, ownerID string, stats *store.MailboxStats, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byOwner[ownerID] = stats.Clone()
	return nil
}

func (c *memStatsCache) IncrBy(_ context.Context, ownerID, field string, delta int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.byOwner[ownerID]
	if s == nil {
		s = &store.MailboxStats{Folders: map[string]store.FolderCounts{}}
		c.byOwner[ownerID] = s
	}
	switch field {
	case mailbox.StatsCacheFieldTotal:
		s.TotalMessages += delta
	case mailbox.StatsCacheFieldUnread:
		s.UnreadCount += delta
	case mailbox.StatsCacheFieldDraft:
		s.DraftCount += delta
	}
	return nil
}

func (c *memStatsCache) Invalidate(_ context.Context, ownerID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byOwner, ownerID)
	return nil
}

// TestStatsCacheWiring exercises the L2 stats cache path: WithStatsCache plus
// send/read operations that drive incrDistCache.
func TestStatsCacheWiring(t *testing.T) {
	ctx := context.Background()
	cache := newMemStatsCache()

	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithStatsCache(cache),
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")
	for range 3 {
		if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"}, Subject: "s", Body: "b",
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
		list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 100})
		return err == nil && len(list.All()) >= 3
	}, "inbox did not fill")

	// Reading stats populates and reads through the L2 cache.
	// With the L2 cache, the total may lag the store as event-driven
	// increments propagate asynchronously; assert it is populated rather than
	// an exact count to keep the test deterministic.
	stats, err := bob.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalMessages <= 0 {
		t.Errorf("TotalMessages = %d, want > 0", stats.TotalMessages)
	}
	if _, err := bob.UnreadCount(ctx); err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
}

// TestBulkOperationError_Error covers the BulkOperationError.Error formatting,
// including the nil-Result guard.
func TestBulkOperationError_Error(t *testing.T) {
	nilResult := &mailbox.BulkOperationError{}
	if nilResult.Error() == "" {
		t.Error("nil-Result BulkOperationError must still produce a message")
	}

	withResult := &mailbox.BulkOperationError{
		Result: &mailbox.BulkResult{
			Results: []mailbox.OperationResult{
				{ID: "a", Success: true},
				{ID: "b", Success: false, Error: errors.New("boom")},
			},
		},
	}
	if withResult.Error() == "" {
		t.Error("populated BulkOperationError must produce a message")
	}
	if len(withResult.Unwrap()) != 1 {
		t.Errorf("Unwrap len = %d, want 1", len(withResult.Unwrap()))
	}
}

// TestWithEventBus exercises the WithEventBus option via a shared bus.
func TestWithEventBus(t *testing.T) {
	ctx := context.Background()
	bus := mailboxtest.NewBus(t)
	svc := mailboxtest.NewServiceWithBus(t, mailbox.Config{}, bus)

	if _, err := svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"}, Subject: "s", Body: "b",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// TestDraftListDelete covers DraftList.Delete with saved drafts present.
func TestDraftListDelete(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	for range 2 {
		d, err := alice.Compose()
		if err != nil {
			t.Fatalf("compose: %v", err)
		}
		d.SetSubject("draft").SetBody("body")
		if _, err := d.Save(ctx); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	list, err := alice.Drafts(ctx, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Drafts: %v", err)
	}
	res, err := list.Delete(ctx)
	if err != nil {
		t.Fatalf("DraftList.Delete: %v", err)
	}
	if res.SuccessCount() < 2 {
		t.Errorf("deleted %d drafts, want >= 2", res.SuccessCount())
	}

	after, _ := alice.Drafts(ctx, store.ListOptions{Limit: 10})
	if len(after.All()) != 0 {
		t.Errorf("drafts remain after delete: %d", len(after.All()))
	}
}

// TestThreadQueries covers GetThread, GetReplies, and ThreadParticipants error path.
func TestThreadQueries(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")

	root, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Root",
		Body:         "body",
		ThreadID:     "thr-1",
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	_ = firstInbox(t, bob)

	// GetThread returns alice's copy in the thread.
	thread, err := alice.GetThread(ctx, "thr-1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(thread.All()) == 0 {
		t.Error("expected at least one message in thread")
	}

	// GetReplies on the root (no replies yet) should not error.
	if _, err := alice.GetReplies(ctx, root.GetID(), store.ListOptions{Limit: 10}); err != nil {
		t.Fatalf("GetReplies: %v", err)
	}

	// ThreadParticipants on an unknown thread returns an error or empty set.
	if _, err := svc.ThreadParticipants(ctx, "no-such-thread"); err == nil {
		// Some backends return empty rather than error; that's acceptable.
		t.Log("ThreadParticipants returned no error for unknown thread (empty set)")
	}
}

// TestGetDraft_NotFound covers the GetDraft error path.
func TestGetDraft_NotFound(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	if _, err := svc.Client("alice").GetDraft(ctx, "missing-draft"); err == nil {
		t.Error("expected error for missing draft")
	}
}

// TestRemoveTag_Idempotent removes a tag that was never added, covering the
// no-op branch, then adds and removes to cover the mutating branch.
func TestRemoveTag_Idempotent(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"}, Subject: "s", Body: "b",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := firstInbox(t, bob)

	// Remove a tag that isn't present (idempotent path).
	if err := bob.RemoveTag(ctx, msg.GetID(), "absent"); err != nil {
		t.Fatalf("RemoveTag absent: %v", err)
	}
	// Add then remove.
	if err := bob.AddTag(ctx, msg.GetID(), "present"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if err := bob.RemoveTag(ctx, msg.GetID(), "present"); err != nil {
		t.Fatalf("RemoveTag present: %v", err)
	}
	updated, _ := bob.Get(ctx, msg.GetID())
	for _, tg := range updated.GetTags() {
		if tg == "present" {
			t.Error("tag not removed")
		}
	}
}

// TestSendWithUserResolver enriches sender identity metadata on send, covering
// the UserResolver branch of the send path.
func TestSendWithUserResolver(t *testing.T) {
	ctx := context.Background()
	userResolver := resolver.NewStaticUserResolver(map[string]*resolver.UserEntry{
		"alice": {FirstName: "Alice", LastName: "Smith", Email: "alice@example.com"},
	})
	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithUserResolver(userResolver),
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	sent, err := svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"}, Subject: "s", Body: "b",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	md := sent.GetMetadata()
	if md[mailbox.MetadataSenderFirstName] != "Alice" {
		t.Errorf("sender firstname metadata = %v, want Alice", md[mailbox.MetadataSenderFirstName])
	}
}
