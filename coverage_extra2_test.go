package mailbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rbaliyan/event/v3/transport/channel"
	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// noBulkStore embeds the store.Store interface (which does NOT include
// store.BulkUpdater), so a service built on it cannot use the native bulk
// fast path — forcing the paginated slow path in the filter-based bulk ops.
type noBulkStore struct {
	store.Store
}

func newNoBulkService(t *testing.T) mailbox.Service {
	t.Helper()
	inner := memory.New()
	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(&noBulkStore{Store: inner}),
		mailbox.WithEventTransport(channel.New()),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// TestFilterBulkOps_SlowPath drives the paginated fallback of the filter-based
// bulk operations using a store without BulkUpdater support.
func TestFilterBulkOps_SlowPath(t *testing.T) {
	ctx := context.Background()
	svc := newNoBulkService(t)

	alice := svc.Client("alice")
	bob := svc.Client("bob")
	for range 4 {
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
		return err == nil && len(list.All()) >= 4
	}, "inbox did not fill")

	if n, err := bob.UpdateByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, mailbox.MarkRead()); err != nil || n != 4 {
		t.Errorf("UpdateByFilter slow path = %d, %v; want 4", n, err)
	}
	if n, err := bob.TagByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, "tg"); err != nil || n != 4 {
		t.Errorf("TagByFilter slow path = %d, %v; want 4", n, err)
	}
	if n, err := bob.UntagByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, "tg"); err != nil || n != 4 {
		t.Errorf("UntagByFilter slow path = %d, %v; want 4", n, err)
	}
	if n, err := bob.MoveByFilter(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, store.FolderArchived); err != nil || n != 4 {
		t.Errorf("MoveByFilter slow path = %d, %v; want 4", n, err)
	}
	if n, err := bob.DeleteByFilter(ctx, []store.Filter{store.InFolder(store.FolderArchived)}); err != nil || n != 4 {
		t.Errorf("DeleteByFilter slow path = %d, %v; want 4", n, err)
	}
}

// TestMarkAllRead_SlowPath drives MarkAllRead without a BulkReadMarker fast path.
func TestMarkAllRead_SlowPath(t *testing.T) {
	ctx := context.Background()
	svc := newNoBulkService(t)

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

	if n, err := bob.MarkAllRead(ctx, store.FolderInbox); err != nil || n != 3 {
		t.Errorf("MarkAllRead slow path = %d, %v; want 3", n, err)
	}
}

// failingPlugin returns an error from Init to exercise PluginError.
type failingPlugin struct{}

func (failingPlugin) Name() string                  { return "failing" }
func (failingPlugin) Init(context.Context) error    { return errors.New("init boom") }
func (failingPlugin) Close(context.Context) error   { return nil }

// TestPluginError verifies a failing plugin Init surfaces a PluginError.
func TestPluginError(t *testing.T) {
	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithPlugin(failingPlugin{}),
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	connectErr := svc.Connect(context.Background())
	if connectErr == nil {
		_ = svc.Close(context.Background())
		t.Fatal("expected Connect to fail due to plugin init error")
	}
	var pe *mailbox.PluginError
	if !errors.As(connectErr, &pe) {
		t.Fatalf("expected *PluginError, got %T: %v", connectErr, connectErr)
	}
	if pe.Error() == "" {
		t.Error("empty PluginError.Error()")
	}
	if !errors.Is(pe, errors.Unwrap(pe)) {
		t.Error("PluginError should unwrap to its inner error")
	}
}

// staticLister returns a fixed set of user IDs.
type staticLister struct{ ids []string }

func (l staticLister) ListUsers(context.Context) ([]string, error) { return l.ids, nil }

// TestRunQuotaEnforcement exercises the QuotaUserLister-driven enforcement entry.
func TestRunQuotaEnforcement(t *testing.T) {
	ctx := context.Background()

	t.Run("no lister configured", func(t *testing.T) {
		svc := newTestService()
		defer svc.Close(ctx)
		if _, err := svc.RunQuotaEnforcement(ctx); !errors.Is(err, mailbox.ErrQuotaUserListerNotConfigured) {
			t.Errorf("expected ErrQuotaUserListerNotConfigured, got %v", err)
		}
	})

	t.Run("with lister", func(t *testing.T) {
		svc, err := mailbox.New(mailbox.Config{
			QuotaUserLister: staticLister{ids: []string{"bob"}},
		},
			mailbox.WithStore(memory.New()),
			mailbox.WithEventTransport(channel.New()),
			mailbox.WithGlobalQuota(mailbox.QuotaPolicy{
				MaxMessages:     1,
				ExceedAction:    mailbox.QuotaActionDeleteOldest,
				DeleteOlderThan: time.Nanosecond,
			}),
		)
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		for range 3 {
			if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
				RecipientIDs: []string{"bob"}, Subject: "s", Body: "b",
			}); err != nil {
				t.Fatalf("send: %v", err)
			}
		}
		res, err := svc.RunQuotaEnforcement(ctx)
		if err != nil {
			t.Fatalf("RunQuotaEnforcement: %v", err)
		}
		if res.UsersChecked != 1 {
			t.Errorf("UsersChecked = %d, want 1", res.UsersChecked)
		}
	})
}

// TestOptionsWiring constructs a service exercising the option setters that are
// otherwise unused in the test suite.
func TestOptionsWiring(t *testing.T) {
	ctx := context.Background()

	var publishFailures int
	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithEventErrorsFatal(false),
		mailbox.WithNotificationCoalescing(true),
		mailbox.WithServiceName("test-svc"),
		mailbox.WithEventPublishFailureHandler(func(string, error) { publishFailures++ }),
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	// Basic operation should still work with all options wired.
	if _, err := svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"}, Subject: "s", Body: "b",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// TestDraftList covers the DraftList accessors and bulk Send/Delete.
func TestDraftList(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")

	// Save two drafts.
	for i := range 2 {
		d, err := alice.Compose()
		if err != nil {
			t.Fatalf("compose: %v", err)
		}
		d.SetSubject("draft").SetBody("body").SetRecipients("bob")
		_ = i
		if _, err := d.Save(ctx); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	list, err := alice.Drafts(ctx, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Drafts: %v", err)
	}
	if list.Total() < 2 {
		t.Errorf("Total = %d, want >= 2", list.Total())
	}
	_ = list.HasMore()
	_ = list.NextCursor()
	if len(list.All()) < 2 {
		t.Errorf("All = %d, want >= 2", len(list.All()))
	}
	for _, d := range list.All() {
		if d.ID() == "" {
			t.Error("saved draft has empty ID")
		}
	}

	// Send all drafts in the list.
	sendRes, err := list.Send(ctx)
	if err != nil {
		t.Fatalf("DraftList.Send: %v", err)
	}
	if len(sendRes.SentMessages()) < 2 {
		t.Errorf("SentMessages = %d, want >= 2", len(sendRes.SentMessages()))
	}

	// A fresh list (now empty) Delete is a no-op without error.
	empty, err := alice.Drafts(ctx, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Drafts: %v", err)
	}
	if _, err := empty.Delete(ctx); err != nil {
		t.Errorf("Delete on empty draft list: %v", err)
	}
}

// TestCleanupExpiredMessages_TTLWithAttachments drives the attachment-aware TTL
// cleanup path: a message with a short TTL and an attachment manager is cleaned
// up after expiry, releasing attachment refs.
func TestCleanupExpiredMessages_TTLWithAttachments(t *testing.T) {
	ctx := context.Background()

	svc, err := mailbox.New(mailbox.Config{MinTTL: time.Nanosecond},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithAttachmentManager(noopAttachmentManager{}),
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
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "ttl",
		Body:         "body",
		Attachments:  []store.Attachment{draftAtt{name: "f.txt", contentType: "text/plain", size: 1}},
		TTL:          time.Millisecond,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Wait until bob's copy is past its TTL.
	mailboxtest.Eventually(t, time.Second, time.Millisecond, func() bool {
		list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 10})
		if err != nil || len(list.All()) == 0 {
			return false
		}
		return time.Since(list.All()[0].GetCreatedAt()) > time.Millisecond
	}, "message did not age past TTL")

	res, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("CleanupExpiredMessages: %v", err)
	}
	if res.DeletedCount == 0 {
		t.Error("expected at least one TTL-expired message deleted")
	}
}
