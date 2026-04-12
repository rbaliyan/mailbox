package mailbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TrashCleanupInterval != 0 {
		t.Errorf("TrashCleanupInterval = %v, want 0", cfg.TrashCleanupInterval)
	}
	if cfg.ExpiredMessageCleanupInterval != 0 {
		t.Errorf("ExpiredMessageCleanupInterval = %v, want 0", cfg.ExpiredMessageCleanupInterval)
	}
	if cfg.QuotaEnforcementInterval != 0 {
		t.Errorf("QuotaEnforcementInterval = %v, want 0", cfg.QuotaEnforcementInterval)
	}
	if cfg.QuotaUserLister != nil {
		t.Error("QuotaUserLister should be nil")
	}
}

func TestNew(t *testing.T) {
	t.Run("requires store", func(t *testing.T) {
		_, err := New(DefaultConfig())
		if !errors.Is(err, ErrStoreRequired) {
			t.Errorf("expected ErrStoreRequired, got %v", err)
		}
	})

	t.Run("creates service with default config", func(t *testing.T) {
		svc, err := New(DefaultConfig(), WithStore(memory.New()))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svc == nil {
			t.Fatal("expected non-nil service")
		}
	})

	t.Run("NewService delegates to New with DefaultConfig", func(t *testing.T) {
		svc, err := NewService(WithStore(memory.New()))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svc == nil {
			t.Fatal("expected non-nil service")
		}
	})
}

func TestNewWithConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("connect and close with background tasks", func(t *testing.T) {
		cfg := Config{
			TrashCleanupInterval:          100 * time.Millisecond,
			ExpiredMessageCleanupInterval: 100 * time.Millisecond,
		}
		svc, err := New(cfg, WithStore(memory.New()))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect failed: %v", err)
		}

		// Let background tasks tick at least once
		time.Sleep(150 * time.Millisecond)

		// Close should stop goroutines and not hang
		if err := svc.Close(ctx); err != nil {
			t.Fatalf("close failed: %v", err)
		}
	})

	t.Run("zero intervals start no goroutines", func(t *testing.T) {
		svc, err := New(DefaultConfig(), WithStore(memory.New()))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect failed: %v", err)
		}
		// Close should complete immediately with no goroutines to wait for
		if err := svc.Close(ctx); err != nil {
			t.Fatalf("close failed: %v", err)
		}
	})
}

func TestBackgroundTrashCleanup(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()

	cfg := Config{
		TrashCleanupInterval: 100 * time.Millisecond,
	}
	// Use default trash retention (30 days) since minimum is 24 hours.
	svc, err := New(cfg, WithStore(memStore))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Create a message directly in trash (matches existing cleanup test pattern).
	msg, err := memStore.CreateMessage(ctx, store.MessageData{
		OwnerID:  "alice",
		SenderID: "alice",
		Subject:  "Test",
		Body:     "Body",
		FolderID: store.FolderTrash,
		Status:   store.MessageStatusSent,
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Age past default trash retention (31 days > 30 days default)
	memStore.AgeMessagesByID(31*24*time.Hour, msg.GetID())

	// Wait for background cleanup to tick
	time.Sleep(250 * time.Millisecond)

	// Verify the trashed message was cleaned up
	_, storeErr := memStore.Get(ctx, msg.GetID())
	if !errors.Is(storeErr, store.ErrNotFound) {
		t.Errorf("expected store.ErrNotFound after background trash cleanup, got %v", storeErr)
	}

	if err := svc.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestBackgroundExpiredMessageCleanup(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()

	cfg := Config{
		ExpiredMessageCleanupInterval: 100 * time.Millisecond,
	}
	svc, err := New(cfg,
		WithStore(memStore),
		WithMessageRetention(24*time.Hour),
	)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Create a message via store and age it past retention.
	msg, err := memStore.CreateMessage(ctx, store.MessageData{
		OwnerID:  "alice",
		SenderID: "alice",
		Subject:  "Expiring",
		Body:     "Body",
		FolderID: store.FolderSent,
		Status:   store.MessageStatusSent,
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Age past retention
	memStore.AgeMessages(48 * time.Hour)

	// Wait for background cleanup
	time.Sleep(250 * time.Millisecond)

	// Message should be deleted
	_, storeErr := memStore.Get(ctx, msg.GetID())
	if !errors.Is(storeErr, store.ErrNotFound) {
		t.Errorf("expected store.ErrNotFound after background expired cleanup, got %v", storeErr)
	}

	if err := svc.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// staticUserLister is a simple QuotaUserLister for tests.
type staticUserLister struct {
	users []string
}

func (l *staticUserLister) ListUsers(_ context.Context) ([]string, error) {
	return l.users, nil
}

// countingUserLister counts how many times ListUsers is called.
type countingUserLister struct {
	users []string
	calls int64
}

func (l *countingUserLister) ListUsers(_ context.Context) ([]string, error) {
	atomic.AddInt64(&l.calls, 1)
	return l.users, nil
}

func TestBackgroundQuotaEnforcement(t *testing.T) {
	ctx := context.Background()

	t.Run("runs when lister is provided", func(t *testing.T) {
		lister := &countingUserLister{users: []string{"alice"}}

		cfg := Config{
			QuotaEnforcementInterval: 50 * time.Millisecond,
			QuotaUserLister:          lister,
		}
		svc, err := New(cfg,
			WithStore(memory.New()),
			WithGlobalQuota(QuotaPolicy{
				MaxMessages:     1000,
				ExceedAction:    QuotaActionDeleteOldest,
				DeleteOlderThan: 24 * time.Hour,
			}),
		)
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}

		// Wait for at least one enforcement tick
		time.Sleep(120 * time.Millisecond)

		calls := atomic.LoadInt64(&lister.calls)
		if calls == 0 {
			t.Error("expected QuotaUserLister.ListUsers to be called at least once")
		}

		if err := svc.Close(ctx); err != nil {
			t.Fatalf("close: %v", err)
		}
	})

	t.Run("skipped when lister is nil", func(t *testing.T) {
		cfg := Config{
			QuotaEnforcementInterval: 50 * time.Millisecond,
			// QuotaUserLister is nil
		}
		svc, err := New(cfg, WithStore(memory.New()))
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}

		// Should not panic or hang
		time.Sleep(100 * time.Millisecond)

		if err := svc.Close(ctx); err != nil {
			t.Fatalf("close: %v", err)
		}
	})
}

func TestBackgroundCloseStopsGoroutines(t *testing.T) {
	ctx := context.Background()

	cfg := Config{
		TrashCleanupInterval:          10 * time.Millisecond,
		ExpiredMessageCleanupInterval: 10 * time.Millisecond,
	}
	svc, err := New(cfg, WithStore(memory.New()))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Close should return promptly, not hang on goroutines
	done := make(chan error, 1)
	go func() {
		done <- svc.Close(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("close failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5 seconds — background goroutines may be stuck")
	}
}

func TestSendStillWorksWithBackgroundTasks(t *testing.T) {
	ctx := context.Background()

	cfg := Config{
		TrashCleanupInterval:          100 * time.Millisecond,
		ExpiredMessageCleanupInterval: 100 * time.Millisecond,
	}
	svc, err := New(cfg, WithStore(memory.New()))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")

	msg, err := alice.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if msg.GetSubject() != "Hello" {
		t.Errorf("subject = %q, want Hello", msg.GetSubject())
	}

	inbox, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inbox.All()) != 1 {
		t.Errorf("expected 1 inbox message, got %d", len(inbox.All()))
	}
}
