package postgres

import (
	"context"
	"fmt"

	"github.com/lib/pq"
	"github.com/rbaliyan/mailbox/store"
)

// CountByFolders returns message counts and unread counts for the given folders.
// Uses a single GROUP BY query with array filter for efficient batch counting.
func (s *Store) CountByFolders(ctx context.Context, ownerID string, folderIDs []string) (map[string]store.FolderCounts, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`
		SELECT folder_id, COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN NOT is_read THEN 1 ELSE 0 END), 0) AS unread
		FROM %s
		WHERE owner_id = $1 AND is_draft = false AND folder_id = ANY($2)
		GROUP BY folder_id
	`, s.opts.table)

	rows, err := s.db.QueryContext(ctx, query, ownerID, pq.Array(folderIDs))
	if err != nil {
		return nil, fmt.Errorf("count by folders: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]store.FolderCounts, len(folderIDs))
	for rows.Next() {
		var folderID string
		var total, unread int64
		if err := rows.Scan(&folderID, &total, &unread); err != nil {
			return nil, fmt.Errorf("scan folder counts: %w", err)
		}
		counts[folderID] = store.FolderCounts{Total: total, Unread: unread}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate folder counts: %w", err)
	}

	return counts, nil
}

// FindWithCount retrieves messages and total count in a single operation.
// Delegates to Find + Count for simplicity.
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
func (s *Store) ListDistinctFolders(ctx context.Context, ownerID string) ([]string, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`
		SELECT DISTINCT folder_id FROM %s
		WHERE owner_id = $1 AND is_draft = false
	`, s.opts.table)

	rows, err := s.db.QueryContext(ctx, query, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list distinct folders: %w", err)
	}
	defer rows.Close()

	var folders []string
	for rows.Next() {
		var folderID string
		if err := rows.Scan(&folderID); err != nil {
			return nil, fmt.Errorf("scan folder: %w", err)
		}
		folders = append(folders, folderID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate folders: %w", err)
	}

	return folders, nil
}

// MailboxStats returns aggregate statistics for a user's mailbox using a single
// query with a CTE for consistent point-in-time results.
func (s *Store) MailboxStats(ctx context.Context, ownerID string) (*store.MailboxStats, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	stats := &store.MailboxStats{
		Folders: make(map[string]store.FolderCounts),
	}

	// Single query: returns totals row (folder_id='') followed by per-folder rows.
	// The CTE ensures both aggregations see the same snapshot.
	query := fmt.Sprintf(`
		WITH msgs AS (
			SELECT is_draft, is_read, folder_id
			FROM %s
			WHERE owner_id = $1
		)
		SELECT '' AS folder_id,
			COALESCE(SUM(CASE WHEN NOT is_draft THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN NOT is_draft AND NOT is_read THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN is_draft THEN 1 ELSE 0 END), 0)
		FROM msgs
		UNION ALL
		SELECT folder_id,
			COUNT(*),
			COALESCE(SUM(CASE WHEN NOT is_read THEN 1 ELSE 0 END), 0),
			0
		FROM msgs
		WHERE NOT is_draft
		GROUP BY folder_id
	`, s.opts.table)

	rows, err := s.db.QueryContext(ctx, query, ownerID)
	if err != nil {
		return nil, fmt.Errorf("query mailbox stats: %w", err)
	}
	defer rows.Close()

	first := true
	for rows.Next() {
		var folderID string
		var col1, col2, col3 int64
		if err := rows.Scan(&folderID, &col1, &col2, &col3); err != nil {
			return nil, fmt.Errorf("scan mailbox stats: %w", err)
		}
		if first {
			// First row is the totals row
			stats.TotalMessages = col1
			stats.UnreadCount = col2
			stats.DraftCount = col3
			first = false
		} else {
			// Subsequent rows are per-folder breakdowns
			stats.Folders[folderID] = store.FolderCounts{Total: col1, Unread: col2}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailbox stats: %w", err)
	}

	return stats, nil
}
