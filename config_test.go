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

// eventually polls cond until it returns true or the timeout elapses, failing
// the test via t.Fatalf with msg if the condition never holds. It is the
// in-package equivalent of mailboxtest.Eventually (which cannot be imported
// here because mailboxtest depends on this package). Use it instead of
// time.Sleep to synchronize on background goroutines: it returns as soon as the
// condition is met, so it is both faster and not flaky under load.
func eventually(t *testing.T, timeout, interval time.Duration, cond func() bool, msg string) {
	t.Helper()
	if cond() {
		return
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			break
		}
	}
	t.Fatalf("condition not met within %s: %s", timeout, msg)
}

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

	t.Run("creates service with custom config", func(t *testing.T) {
		cfg := Config{
			TrashRetention: 7 * 24 * time.Hour,
		}
		svc, err := New(cfg, WithStore(memory.New()))
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
		// Seed an aged trash message before Connect so the background trash
		// cleanup goroutine has observable work to do.
		memStore := memory.New()
		if err := memStore.Connect(ctx); err != nil {
			t.Fatalf("connect store: %v", err)
		}
		msg, err := memStore.CreateMessage(ctx, store.MessageData{
			OwnerID:  "alice",
			SenderID: "alice",
			Subject:  "Aged",
			Body:     "Body",
			FolderID: store.FolderTrash,
			Status:   store.MessageStatusSent,
		})
		if err != nil {
			t.Fatalf("create message: %v", err)
		}
		memStore.AgeMessagesByID(31*24*time.Hour, msg.GetID())
		if err := memStore.Close(ctx); err != nil {
			t.Fatalf("close store: %v", err)
		}

		cfg := Config{
			TrashCleanupInterval:          50 * time.Millisecond,
			ExpiredMessageCleanupInterval: 50 * time.Millisecond,
		}
		svc, err := New(cfg, WithStore(memStore))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect failed: %v", err)
		}

		// Background trash cleanup must remove the aged message at least once,
		// proving the goroutine actually ran.
		eventually(t, 2*time.Second, 10*time.Millisecond, func() bool {
			_, getErr := memStore.Get(ctx, msg.GetID())
			return errors.Is(getErr, store.ErrNotFound)
		}, "background trash cleanup never removed the aged message")

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

	// Connect store, create+age data, then disconnect.
	// The service's Connect will reconnect the store.
	// This avoids a data race between AgeMessagesByID and background goroutines.
	if err := memStore.Connect(ctx); err != nil {
		t.Fatalf("connect store: %v", err)
	}
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
	memStore.AgeMessagesByID(31*24*time.Hour, msg.GetID())
	if err := memStore.Close(ctx); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Now start the service — goroutines start after Connect, data already aged.
	cfg := Config{
		TrashCleanupInterval: 100 * time.Millisecond,
	}
	svc, err := New(cfg, WithStore(memStore))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Wait for background cleanup to remove the aged trash message.
	eventually(t, 2*time.Second, 10*time.Millisecond, func() bool {
		_, getErr := memStore.Get(ctx, msg.GetID())
		return errors.Is(getErr, store.ErrNotFound)
	}, "background trash cleanup never removed the aged message")

	if err := svc.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestBackgroundExpiredMessageCleanup(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()

	// Connect store, create+age data, then disconnect.
	// The service's Connect will reconnect the store.
	// This avoids a data race between AgeMessages and background goroutines.
	if err := memStore.Connect(ctx); err != nil {
		t.Fatalf("connect store: %v", err)
	}
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
	memStore.AgeMessages(48 * time.Hour)
	if err := memStore.Close(ctx); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Now start the service — goroutines start after Connect, data already aged.
	cfg := Config{
		ExpiredMessageCleanupInterval: 100 * time.Millisecond,
		MessageRetention:              24 * time.Hour,
	}
	svc, err := New(cfg,
		WithStore(memStore),
	)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Wait for background cleanup to remove the expired message.
	eventually(t, 2*time.Second, 10*time.Millisecond, func() bool {
		_, getErr := memStore.Get(ctx, msg.GetID())
		return errors.Is(getErr, store.ErrNotFound)
	}, "background expired cleanup never removed the message")

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

		// The enforcement goroutine must call ListUsers at least once.
		eventually(t, 2*time.Second, 10*time.Millisecond, func() bool {
			return atomic.LoadInt64(&lister.calls) > 0
		}, "expected QuotaUserLister.ListUsers to be called at least once")

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

		// With a nil lister the enforcement goroutine must skip its work without
		// panicking or hanging. Close waits for the goroutine to drain, so a
		// clean Close proves it stopped safely.
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
