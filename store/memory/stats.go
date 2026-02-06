package memory

import (
	"context"

	"github.com/rbaliyan/mailbox/store"
)

// CountByFolders returns message counts and unread counts for the given folders.
// Implements store.FolderCounter for optimized batch counting.
func (s *Store) CountByFolders(ctx context.Context, ownerID string, folderIDs []string) (map[string]store.FolderCounts, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	folderSet := make(map[string]bool, len(folderIDs))
	for _, id := range folderIDs {
		folderSet[id] = true
	}

	counts := make(map[string]store.FolderCounts, len(folderIDs))
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.isDraft || m.ownerID != ownerID {
			return true
		}
		if !folderSet[m.folderID] {
			return true
		}
		c := counts[m.folderID]
		c.Total++
		if !m.isRead {
			c.Unread++
		}
		counts[m.folderID] = c
		return true
	})

	return counts, nil
}

// FindWithCount retrieves messages and total count in a single pass.
// Implements store.FindWithCounter for optimized list operations.
func (s *Store) FindWithCount(ctx context.Context, filters []store.Filter, opts store.ListOptions) (*store.MessageList, int64, error) {
	list, err := s.Find(ctx, filters, opts)
	if err != nil {
		return nil, 0, err
	}
	count, err := s.Count(ctx, filters)
	if err != nil {
		return nil, 0, err
	}
	return list, count, nil
}

// ListDistinctFolders returns all distinct folder IDs for a user's non-deleted messages.
// Implements store.FolderLister for custom folder discovery.
func (s *Store) ListDistinctFolders(ctx context.Context, ownerID string) ([]string, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.isDraft || m.ownerID != ownerID {
			return true
		}
		seen[m.folderID] = true
		return true
	})

	folders := make([]string, 0, len(seen))
	for id := range seen {
		folders = append(folders, id)
	}
	return folders, nil
}

// MailboxStats returns aggregate statistics for a user's mailbox in a single pass.
func (s *Store) MailboxStats(ctx context.Context, ownerID string) (*store.MailboxStats, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	stats := &store.MailboxStats{
		Folders: make(map[string]store.FolderCounts),
	}

	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.ownerID != ownerID {
			return true
		}
		if m.isDraft {
			stats.DraftCount++
			return true
		}
		stats.TotalMessages++
		if !m.isRead {
			stats.UnreadCount++
		}
		c := stats.Folders[m.folderID]
		c.Total++
		if !m.isRead {
			c.Unread++
		}
		stats.Folders[m.folderID] = c
		return true
	})

	return stats, nil
}
