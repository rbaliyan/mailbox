package mailbox

import (
	"context"
	"sync"
	"time"

	"github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/mailbox/store"
)

// StatsReader provides access to aggregate mailbox statistics.
type StatsReader interface {
	// Stats returns aggregate statistics for this user's mailbox.
	// Results are cached with event-driven incremental updates and periodic TTL refresh.
	Stats(ctx context.Context) (*store.MailboxStats, error)
	// UnreadCount returns the total unread message count for this user's mailbox.
	// This is a convenience method equivalent to calling Stats() and reading UnreadCount.
	UnreadCount(ctx context.Context) (int64, error)
}

// statsEntry holds a cached stats snapshot for a single user.
type statsEntry struct {
	mu        sync.Mutex
	stats     *store.MailboxStats
	updatedAt time.Time
}

// getOrRefreshStats returns cached stats if within TTL, otherwise refreshes from the store.
// When no event transport is configured (statsCacheEnabled=false), always fetches directly
// from the store since incremental event updates won't keep the cache accurate.
func (s *service) getOrRefreshStats(ctx context.Context, ownerID string) (*store.MailboxStats, error) {
	// Without an event transport, the cache won't receive incremental updates
	// and would serve stale data. Always fetch from the store.
	if !s.statsCacheEnabled {
		return s.store.MailboxStats(ctx, ownerID)
	}

	now := time.Now()

	// LoadOrStore ensures we always work with a single entry per ownerID.
	// This prevents the race where two goroutines both see a stale/missing entry
	// and both fetch from the store, with the second overwriting event updates
	// applied to the first.
	val, _ := s.statsCache.LoadOrStore(ownerID, &statsEntry{})
	entry := val.(*statsEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Fast path: return cached entry if within TTL.
	if entry.stats != nil && now.Sub(entry.updatedAt) < s.opts.statsRefreshInterval {
		return entry.stats.Clone(), nil
	}

	// Slow path: fetch from store while holding the lock.
	// This serializes store fetches for the same ownerID and ensures
	// event-driven updates aren't lost between fetch and cache store.
	stats, err := s.store.MailboxStats(ctx, ownerID)
	if err != nil {
		return nil, err
	}

	entry.stats = stats
	entry.updatedAt = now

	return stats.Clone(), nil
}

// updateCachedStats applies a mutation to a cached stats entry if it exists.
// If no cache entry exists for the user, this is a no-op.
func (s *service) updateCachedStats(ownerID string, fn func(stats *store.MailboxStats)) {
	val, ok := s.statsCache.Load(ownerID)
	if !ok {
		return
	}
	entry := val.(*statsEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.stats != nil {
		if entry.stats.Folders == nil {
			entry.stats.Folders = make(map[string]store.FolderCounts)
		}
		fn(entry.stats)
	}
}

// onMessageSent handles the MessageSent event for stats cache updates.
// Increments total and folder counts for sender and each recipient.
func (s *service) onMessageSent(_ context.Context, _ event.Event[MessageSentEvent], data MessageSentEvent) error {
	// Update sender's stats: new message in sent folder.
	s.updateCachedStats(data.SenderID, func(stats *store.MailboxStats) {
		stats.TotalMessages++
		c := stats.Folders[store.FolderSent]
		c.Total++
		stats.Folders[store.FolderSent] = c
	})

	// Update each recipient's stats: new unread message in inbox.
	for _, recipientID := range data.RecipientIDs {
		s.updateCachedStats(recipientID, func(stats *store.MailboxStats) {
			stats.TotalMessages++
			stats.UnreadCount++
			c := stats.Folders[store.FolderInbox]
			c.Total++
			c.Unread++
			stats.Folders[store.FolderInbox] = c
		})
	}

	return nil
}

// onMessageRead handles the MessageRead event for stats cache updates.
// Decrements global and per-folder unread counts.
func (s *service) onMessageRead(_ context.Context, _ event.Event[MessageReadEvent], data MessageReadEvent) error {
	s.updateCachedStats(data.UserID, func(stats *store.MailboxStats) {
		if stats.UnreadCount > 0 {
			stats.UnreadCount--
		}
		if data.FolderID != "" {
			c := stats.Folders[data.FolderID]
			if c.Unread > 0 {
				c.Unread--
				stats.Folders[data.FolderID] = c
			}
		}
	})
	return nil
}

// onMessageDeleted handles the MessageDeleted event for stats cache updates.
// Decrements total, per-folder total, and unread counts as appropriate.
func (s *service) onMessageDeleted(_ context.Context, _ event.Event[MessageDeletedEvent], data MessageDeletedEvent) error {
	s.updateCachedStats(data.UserID, func(stats *store.MailboxStats) {
		if stats.TotalMessages > 0 {
			stats.TotalMessages--
		}
		if data.WasUnread && stats.UnreadCount > 0 {
			stats.UnreadCount--
		}
		if data.FolderID != "" {
			c := stats.Folders[data.FolderID]
			if c.Total > 0 {
				c.Total--
			}
			if data.WasUnread && c.Unread > 0 {
				c.Unread--
			}
			stats.Folders[data.FolderID] = c
		}
	})
	return nil
}

// onMessageMoved handles the MessageMoved event for stats cache updates.
// Decrements the source folder total and increments the destination folder total.
func (s *service) onMessageMoved(_ context.Context, _ event.Event[MessageMovedEvent], data MessageMovedEvent) error {
	s.updateCachedStats(data.UserID, func(stats *store.MailboxStats) {
		if data.FromFolderID != "" {
			c := stats.Folders[data.FromFolderID]
			if c.Total > 0 {
				c.Total--
			}
			stats.Folders[data.FromFolderID] = c
		}
		if data.ToFolderID != "" {
			c := stats.Folders[data.ToFolderID]
			c.Total++
			stats.Folders[data.ToFolderID] = c
		}
	})
	return nil
}

// Stats returns aggregate statistics for this user's mailbox.
func (m *userMailbox) Stats(ctx context.Context) (*store.MailboxStats, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	return m.service.getOrRefreshStats(ctx, m.userID)
}

// UnreadCount returns the total unread message count for this user's mailbox.
func (m *userMailbox) UnreadCount(ctx context.Context) (int64, error) {
	if err := m.checkAccess(); err != nil {
		return 0, err
	}
	stats, err := m.service.getOrRefreshStats(ctx, m.userID)
	if err != nil {
		return 0, err
	}
	return stats.UnreadCount, nil
}
