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
}

// statsEntry holds a cached stats snapshot for a single user.
type statsEntry struct {
	mu        sync.Mutex
	stats     *store.MailboxStats
	updatedAt time.Time
}

// getOrRefreshStats returns cached stats if within TTL, otherwise refreshes from the store.
func (s *service) getOrRefreshStats(ctx context.Context, ownerID string) (*store.MailboxStats, error) {
	now := time.Now()

	// Fast path: return cached entry if within TTL.
	if val, ok := s.statsCache.Load(ownerID); ok {
		entry := val.(*statsEntry)
		entry.mu.Lock()
		if entry.stats != nil && now.Sub(entry.updatedAt) < s.opts.statsRefreshInterval {
			clone := entry.stats.Clone()
			entry.mu.Unlock()
			return clone, nil
		}
		entry.mu.Unlock()
	}

	// Slow path: fetch from store and cache.
	stats, err := s.store.MailboxStats(ctx, ownerID)
	if err != nil {
		return nil, err
	}

	entry := &statsEntry{
		stats:     stats,
		updatedAt: now,
	}
	s.statsCache.Store(ownerID, entry)

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
// Decrements the global unread count. Folder-level unread is corrected at next TTL refresh.
func (s *service) onMessageRead(_ context.Context, _ event.Event[MessageReadEvent], data MessageReadEvent) error {
	s.updateCachedStats(data.UserID, func(stats *store.MailboxStats) {
		if stats.UnreadCount > 0 {
			stats.UnreadCount--
		}
	})
	return nil
}

// onMessageDeleted handles the MessageDeleted event for stats cache updates.
// Decrements total count. Folder-level counts are corrected at next TTL refresh.
func (s *service) onMessageDeleted(_ context.Context, _ event.Event[MessageDeletedEvent], data MessageDeletedEvent) error {
	s.updateCachedStats(data.UserID, func(stats *store.MailboxStats) {
		if stats.TotalMessages > 0 {
			stats.TotalMessages--
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
