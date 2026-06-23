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
	svc, err := New(Config{},
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
	svc, err := New(Config{StatsRefreshInterval: 1 * time.Hour},
		WithStore(memory.New()),
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
	svc, err := New(Config{}, WithStore(memory.New()))
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
		svc, err := New(Config{StatsRefreshInterval: 1 * time.Hour},
			WithStore(memory.New()),
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
		svc, err := New(Config{StatsRefreshInterval: 1 * time.Millisecond},
			WithStore(memory.New()),
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

		// Once the cache TTL expires, Stats refreshes from the store and sees
		// the new message. Poll rather than sleep on the wall clock.
		eventually(t, 2*time.Second, time.Millisecond, func() bool {
			stats2, statsErr := mb.Stats(ctx)
			return statsErr == nil && stats2.TotalMessages == 1
		}, "stats never refreshed to total=1 after TTL expiry")
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

		// Channel transport delivers events asynchronously via goroutines; poll
		// the caches until the event-driven updates land.
		eventually(t, 2*time.Second, 5*time.Millisecond, func() bool {
			aliceStats, _ := alice.Stats(ctx)
			bobStats, _ := bob.Stats(ctx)
			return aliceStats.TotalMessages == 1 &&
				bobStats.TotalMessages == 1 && bobStats.UnreadCount == 1
		}, "send did not update sender and recipient caches")
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

		// Poll until the read event decrements the unread counters.
		eventually(t, 2*time.Second, 5*time.Millisecond, func() bool {
			bobStats, _ := bob.Stats(ctx)
			return bobStats.UnreadCount == 0 &&
				bobStats.Folders[store.FolderInbox].Unread == 0
		}, "read did not decrement unread counts")
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

		// Wait until the send event has propagated to the stats cache: the
		// cached total must agree with the actual number of inbox messages.
		// This avoids snapshotting a stale total while the event is in flight.
		var totalBefore int64
		eventually(t, 2*time.Second, 5*time.Millisecond, func() bool {
			inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
			n := int64(len(inbox.All()))
			if n == 0 {
				return false
			}
			bobStatsBefore, _ := bob.Stats(ctx)
			totalBefore = bobStatsBefore.TotalMessages
			return totalBefore == n
		}, "send event never reached bob's stats cache")

		inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		msgID := inbox.All()[0].GetID()

		if err := bob.Delete(ctx, msgID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if err := bob.PermanentlyDelete(ctx, msgID); err != nil {
			t.Fatalf("permanent delete: %v", err)
		}

		// Poll until the permanent-delete event decrements the cached total.
		eventually(t, 2*time.Second, 5*time.Millisecond, func() bool {
			bobStatsAfter, _ := bob.Stats(ctx)
			return bobStatsAfter.TotalMessages == totalBefore-1
		}, "permanent delete did not decrement cached total")
	})
}

func TestStatsConcurrency(t *testing.T) {
	ctx := context.Background()

	svc, err := New(Config{StatsRefreshInterval: 1 * time.Millisecond},
		WithStore(memory.New()),
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
	svc, _ := New(Config{}, WithStore(memory.New()))
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
