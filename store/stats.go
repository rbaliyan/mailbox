package store

import (
	"context"
	"maps"
)

// MailboxStats holds aggregate statistics for a user's mailbox.
type MailboxStats struct {
	// TotalMessages is the total number of non-draft messages.
	TotalMessages int64
	// UnreadCount is the total number of unread non-draft messages.
	UnreadCount int64
	// DraftCount is the total number of drafts.
	DraftCount int64
	// Folders contains per-folder message counts (non-draft messages only).
	// Keys are folder IDs (e.g., "__inbox", "__sent").
	Folders map[string]FolderCounts
}

// Clone returns a deep copy of the stats.
func (s *MailboxStats) Clone() *MailboxStats {
	c := &MailboxStats{
		TotalMessages: s.TotalMessages,
		UnreadCount:   s.UnreadCount,
		DraftCount:    s.DraftCount,
	}
	if s.Folders != nil {
		c.Folders = make(map[string]FolderCounts, len(s.Folders))
		maps.Copy(c.Folders, s.Folders)
	}
	return c
}

// StatsStore provides aggregate mailbox statistics.
type StatsStore interface {
	// MailboxStats returns aggregate statistics for a user's mailbox.
	// This should be implemented as a single efficient query (e.g., MongoDB $facet,
	// PostgreSQL conditional aggregation) rather than multiple round-trips.
	MailboxStats(ctx context.Context, ownerID string) (*MailboxStats, error)
}
