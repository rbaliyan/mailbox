package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rbaliyan/mailbox/store"
	"github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*Cache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return New(client), mr
}

func TestSetGetRoundTrip(t *testing.T) {
	cache, _ := setup(t)
	ctx := context.Background()

	stats := &store.MailboxStats{
		TotalMessages: 42,
		UnreadCount:   7,
		DraftCount:    3,
		Folders: map[string]store.FolderCounts{
			"__inbox":    {Total: 30, Unread: 5},
			"__sent":     {Total: 10, Unread: 0},
			"__archived": {Total: 2, Unread: 2},
		},
	}

	if err := cache.Set(ctx, "user1", stats, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := cache.Get(ctx, "user1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil stats, got nil")
	}
	if got.TotalMessages != stats.TotalMessages {
		t.Errorf("TotalMessages: want %d got %d", stats.TotalMessages, got.TotalMessages)
	}
	if got.UnreadCount != stats.UnreadCount {
		t.Errorf("UnreadCount: want %d got %d", stats.UnreadCount, got.UnreadCount)
	}
	if got.DraftCount != stats.DraftCount {
		t.Errorf("DraftCount: want %d got %d", stats.DraftCount, got.DraftCount)
	}
	if len(got.Folders) != len(stats.Folders) {
		t.Fatalf("Folders: want %d entries, got %d", len(stats.Folders), len(got.Folders))
	}
	for id, want := range stats.Folders {
		gotFC, ok := got.Folders[id]
		if !ok {
			t.Errorf("missing folder %q", id)
			continue
		}
		if gotFC != want {
			t.Errorf("folder %q: want %+v got %+v", id, want, gotFC)
		}
	}
}

func TestIncrByReflectedInGet(t *testing.T) {
	cache, _ := setup(t)
	ctx := context.Background()

	stats := &store.MailboxStats{
		TotalMessages: 10,
		UnreadCount:   2,
		Folders: map[string]store.FolderCounts{
			"__inbox": {Total: 8, Unread: 2},
		},
	}
	if err := cache.Set(ctx, "user1", stats, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Increment top-level and folder fields.
	if err := cache.IncrBy(ctx, "user1", fieldTotal, 5); err != nil {
		t.Fatalf("IncrBy total: %v", err)
	}
	if err := cache.IncrBy(ctx, "user1", fieldUnread, -1); err != nil {
		t.Fatalf("IncrBy unread: %v", err)
	}
	if err := cache.IncrBy(ctx, "user1", folderTotal("__inbox"), 5); err != nil {
		t.Fatalf("IncrBy folder total: %v", err)
	}
	if err := cache.IncrBy(ctx, "user1", folderUnread("__inbox"), -1); err != nil {
		t.Fatalf("IncrBy folder unread: %v", err)
	}

	got, err := cache.Get(ctx, "user1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil stats")
	}
	if got.TotalMessages != 15 {
		t.Errorf("TotalMessages: want 15 got %d", got.TotalMessages)
	}
	if got.UnreadCount != 1 {
		t.Errorf("UnreadCount: want 1 got %d", got.UnreadCount)
	}
	if got.Folders["__inbox"].Total != 13 {
		t.Errorf("folder total: want 13 got %d", got.Folders["__inbox"].Total)
	}
	if got.Folders["__inbox"].Unread != 1 {
		t.Errorf("folder unread: want 1 got %d", got.Folders["__inbox"].Unread)
	}
}

func TestIncrByNoOpOnMiss(t *testing.T) {
	cache, mr := setup(t)
	ctx := context.Background()

	// Key does not exist — IncrBy must be a no-op.
	if err := cache.IncrBy(ctx, "ghost", fieldTotal, 5); err != nil {
		t.Fatalf("IncrBy: %v", err)
	}

	// Verify no key was created.
	if mr.Exists("mailbox:stats:ghost") {
		t.Fatal("IncrBy should not seed data on cache miss")
	}

	// And Get still returns nil.
	got, err := cache.Get(ctx, "ghost")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil on cache miss, got %+v", got)
	}
}

func TestInvalidate(t *testing.T) {
	cache, mr := setup(t)
	ctx := context.Background()

	stats := &store.MailboxStats{TotalMessages: 1}
	if err := cache.Set(ctx, "user1", stats, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !mr.Exists("mailbox:stats:user1") {
		t.Fatal("expected key to exist after Set")
	}

	if err := cache.Invalidate(ctx, "user1"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	if mr.Exists("mailbox:stats:user1") {
		t.Fatal("expected key to be gone after Invalidate")
	}

	got, err := cache.Get(ctx, "user1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil after Invalidate, got %+v", got)
	}
}
