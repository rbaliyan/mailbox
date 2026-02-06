package mailbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rbaliyan/event/v3/transport/channel"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// setupStatsService creates a service without event transport.
// Stats cache is disabled, so Stats() always fetches from store.
func setupStatsService(t *testing.T) Service {
	t.Helper()
	svc, err := NewService(
		WithStore(memory.New()),
	)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	ctx := context.Background()
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return svc
}

// setupStatsServiceWithEvents creates a service with channel transport and long TTL,
// so stats are only updated by event handlers.
func setupStatsServiceWithEvents(t *testing.T) Service {
	t.Helper()
	svc, err := NewService(
		WithStore(memory.New()),
		WithStatsRefreshInterval(1*time.Hour),
		WithEventTransport(channel.New()),
	)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	ctx := context.Background()
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return svc
}

func TestStats(t *testing.T) {
	ctx := context.Background()
	svc := setupStatsService(t)
	defer svc.Close(ctx)

	t.Run("empty mailbox returns zero stats", func(t *testing.T) {
		mb := svc.Client("alice")
		stats, err := mb.Stats(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stats.TotalMessages != 0 || stats.UnreadCount != 0 || stats.DraftCount != 0 {
			t.Errorf("expected zero stats, got total=%d unread=%d drafts=%d",
				stats.TotalMessages, stats.UnreadCount, stats.DraftCount)
		}
	})

	t.Run("stats reflect sent messages", func(t *testing.T) {
		alice := svc.Client("alice")
		bob := svc.Client("bob")

		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Hello Bob",
			Body:         "How are you?",
		})
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		bobStats, err := bob.Stats(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bobStats.TotalMessages != 1 {
			t.Errorf("expected bob total=1, got %d", bobStats.TotalMessages)
		}
		if bobStats.UnreadCount != 1 {
			t.Errorf("expected bob unread=1, got %d", bobStats.UnreadCount)
		}
		inboxCounts := bobStats.Folders[store.FolderInbox]
		if inboxCounts.Total != 1 || inboxCounts.Unread != 1 {
			t.Errorf("expected inbox total=1 unread=1, got total=%d unread=%d",
				inboxCounts.Total, inboxCounts.Unread)
		}

		aliceStats, err := alice.Stats(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if aliceStats.TotalMessages != 1 {
			t.Errorf("expected alice total=1, got %d", aliceStats.TotalMessages)
		}
		sentCounts := aliceStats.Folders[store.FolderSent]
		if sentCounts.Total != 1 {
			t.Errorf("expected sent total=1, got %d", sentCounts.Total)
		}
	})

	t.Run("stats reflect drafts", func(t *testing.T) {
		mb := svc.Client("carol")
		d := mustCompose(mb)
		d.SetSubject("Draft subject")
		d.SetBody("Draft body")
		if _, err := d.Save(ctx); err != nil {
			t.Fatalf("save draft: %v", err)
		}

		stats, err := mb.Stats(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stats.DraftCount != 1 {
			t.Errorf("expected drafts=1, got %d", stats.DraftCount)
		}
	})
}

func TestStatsCacheDisabledWithoutTransport(t *testing.T) {
	ctx := context.Background()

	// Service without event transport: cache is disabled.
	svc, err := NewService(WithStore(memory.New()))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	mb := svc.Client("user1")

	// First call
	stats1, err := mb.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats1.TotalMessages != 0 {
		t.Fatalf("expected 0, got %d", stats1.TotalMessages)
	}

	// Add message directly to store (bypassing events)
	s := svc.(*service)
	if _, err := s.store.CreateMessage(ctx, store.MessageData{
		OwnerID:  "user1",
		SenderID: "other",
		Subject:  "Test",
		FolderID: store.FolderInbox,
		Status:   store.MessageStatusDelivered,
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Without cache, the second call should see the new message immediately
	stats2, err := mb.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats2.TotalMessages != 1 {
		t.Errorf("expected total=1 (no cache), got %d", stats2.TotalMessages)
	}
}

func TestStatsCaching(t *testing.T) {
	ctx := context.Background()

	t.Run("returns cached result within TTL", func(t *testing.T) {
		svc, err := NewService(
			WithStore(memory.New()),
			WithStatsRefreshInterval(1*time.Hour),
			WithEventTransport(channel.New()),
		)
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer svc.Close(ctx)

		mb := svc.Client("user1")

		// First call seeds cache
		stats1, err := mb.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if stats1.TotalMessages != 0 {
			t.Fatalf("expected 0, got %d", stats1.TotalMessages)
		}

		// Add message directly to store (bypassing events)
		s := svc.(*service)
		if _, err := s.store.CreateMessage(ctx, store.MessageData{
			OwnerID:  "user1",
			SenderID: "other",
			Subject:  "Test",
			FolderID: store.FolderInbox,
			Status:   store.MessageStatusDelivered,
		}); err != nil {
			t.Fatalf("create message: %v", err)
		}

		// Second call should return cached (stale) result
		stats2, err := mb.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if stats2.TotalMessages != 0 {
			t.Errorf("expected cached total=0, got %d", stats2.TotalMessages)
		}
	})

	t.Run("refreshes after TTL expires", func(t *testing.T) {
		svc, err := NewService(
			WithStore(memory.New()),
			WithStatsRefreshInterval(1*time.Millisecond),
			WithEventTransport(channel.New()),
		)
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer svc.Close(ctx)

		mb := svc.Client("user1")

		// Seed cache
		stats1, err := mb.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if stats1.TotalMessages != 0 {
			t.Fatalf("expected 0, got %d", stats1.TotalMessages)
		}

		// Add message directly
		s := svc.(*service)
		if _, err := s.store.CreateMessage(ctx, store.MessageData{
			OwnerID:  "user1",
			SenderID: "other",
			Subject:  "Test",
			FolderID: store.FolderInbox,
			Status:   store.MessageStatusDelivered,
		}); err != nil {
			t.Fatalf("create message: %v", err)
		}

		// Wait for TTL to expire
		time.Sleep(5 * time.Millisecond)

		// Should refresh and see the new message
		stats2, err := mb.Stats(ctx)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if stats2.TotalMessages != 1 {
			t.Errorf("expected refreshed total=1, got %d", stats2.TotalMessages)
		}
	})
}

func TestStatsEventUpdates(t *testing.T) {
	ctx := context.Background()
	svc := setupStatsServiceWithEvents(t)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")

	// Seed both caches
	_, _ = alice.Stats(ctx)
	_, _ = bob.Stats(ctx)

	t.Run("send updates sender and recipient cache", func(t *testing.T) {
		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Event test",
			Body:         "body",
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}

		// Channel transport delivers asynchronously via goroutines
		time.Sleep(50 * time.Millisecond)

		aliceStats, _ := alice.Stats(ctx)
		if aliceStats.TotalMessages != 1 {
			t.Errorf("expected alice total=1, got %d", aliceStats.TotalMessages)
		}

		bobStats, _ := bob.Stats(ctx)
		if bobStats.TotalMessages != 1 {
			t.Errorf("expected bob total=1, got %d", bobStats.TotalMessages)
		}
		if bobStats.UnreadCount != 1 {
			t.Errorf("expected bob unread=1, got %d", bobStats.UnreadCount)
		}
	})

	t.Run("read decrements unread", func(t *testing.T) {
		inbox, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if err != nil {
			t.Fatalf("inbox: %v", err)
		}
		msgs := inbox.All()
		if len(msgs) == 0 {
			t.Fatal("expected messages in bob's inbox")
		}

		if err := bob.UpdateFlags(ctx, msgs[0].GetID(), Flags{Read: boolPtr(true)}); err != nil {
			t.Fatalf("mark read: %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		bobStats, _ := bob.Stats(ctx)
		if bobStats.UnreadCount != 0 {
			t.Errorf("expected bob unread=0, got %d", bobStats.UnreadCount)
		}
		// Verify per-folder unread also decremented
		inboxCounts := bobStats.Folders[store.FolderInbox]
		if inboxCounts.Unread != 0 {
			t.Errorf("expected inbox unread=0, got %d", inboxCounts.Unread)
		}
	})

	t.Run("permanent delete decrements total", func(t *testing.T) {
		// Send another message to bob
		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Delete test",
			Body:         "body",
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		msgs := inbox.All()
		if len(msgs) == 0 {
			t.Fatal("expected messages")
		}

		msgID := msgs[0].GetID()
		bobStatsBefore, _ := bob.Stats(ctx)
		totalBefore := bobStatsBefore.TotalMessages

		if err := bob.Delete(ctx, msgID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if err := bob.PermanentlyDelete(ctx, msgID); err != nil {
			t.Fatalf("permanent delete: %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		bobStatsAfter, _ := bob.Stats(ctx)
		if bobStatsAfter.TotalMessages != totalBefore-1 {
			t.Errorf("expected total=%d, got %d", totalBefore-1, bobStatsAfter.TotalMessages)
		}
	})
}

func TestStatsConcurrency(t *testing.T) {
	ctx := context.Background()

	svc, err := NewService(
		WithStore(memory.New()),
		WithStatsRefreshInterval(1*time.Millisecond),
		WithEventTransport(channel.New()),
	)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	mb := svc.Client("user1")

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_, err := mb.Stats(ctx)
				if err != nil {
					t.Errorf("Stats() error: %v", err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestStatsNotConnected(t *testing.T) {
	svc, _ := NewService(WithStore(memory.New()))
	mb := svc.Client("user1")

	_, err := mb.Stats(context.Background())
	if err != ErrNotConnected {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

func TestStatsClone(t *testing.T) {
	stats := &store.MailboxStats{
		TotalMessages: 10,
		UnreadCount:   5,
		DraftCount:    2,
		Folders: map[string]store.FolderCounts{
			"__inbox": {Total: 8, Unread: 5},
			"__sent":  {Total: 2, Unread: 0},
		},
	}

	clone := stats.Clone()

	// Modify original
	stats.TotalMessages = 100
	stats.Folders["__inbox"] = store.FolderCounts{Total: 999, Unread: 999}

	// Clone should be unaffected
	if clone.TotalMessages != 10 {
		t.Errorf("expected clone total=10, got %d", clone.TotalMessages)
	}
	if clone.Folders["__inbox"].Total != 8 {
		t.Errorf("expected clone inbox total=8, got %d", clone.Folders["__inbox"].Total)
	}
}

func boolPtr(b bool) *bool { return &b }
