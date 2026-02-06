package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/rbaliyan/mailbox/store"
)

func (s *Store) MarkRead(ctx context.Context, id string, read bool) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	var query string
	var args []any
	now := time.Now().UTC()

	if read {
		query = fmt.Sprintf(`
			UPDATE %s SET is_read = true, read_at = $1, updated_at = $2
			WHERE id = $3 AND is_draft = false
		`, s.opts.table)
		args = []any{now, now, id}
	} else {
		query = fmt.Sprintf(`
			UPDATE %s SET is_read = false, read_at = NULL, updated_at = $1
			WHERE id = $2 AND is_draft = false
		`, s.opts.table)
		args = []any{now, id}
	}

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}

	return nil
}

func (s *Store) MoveToFolder(ctx context.Context, id string, folderID string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	if !store.IsValidFolderID(folderID) {
		return store.ErrInvalidFolderID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`
		UPDATE %s SET folder_id = $1, updated_at = $2
		WHERE id = $3 AND is_draft = false
	`, s.opts.table)

	result, err := s.db.ExecContext(ctx, query, folderID, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("move to folder: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}

	return nil
}

func (s *Store) AddTag(ctx context.Context, id string, tagID string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	// Use array_append with CASE to avoid duplicates
	query := fmt.Sprintf(`
		UPDATE %s
		SET tags = CASE
			WHEN $1 = ANY(tags) THEN tags
			ELSE array_append(tags, $1)
		END,
		updated_at = $2
		WHERE id = $3 AND is_draft = false
	`, s.opts.table)

	result, err := s.db.ExecContext(ctx, query, tagID, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("add tag: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}

	return nil
}

func (s *Store) RemoveTag(ctx context.Context, id string, tagID string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`
		UPDATE %s SET tags = array_remove(tags, $1), updated_at = $2
		WHERE id = $3 AND is_draft = false
	`, s.opts.table)

	result, err := s.db.ExecContext(ctx, query, tagID, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}

	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`
		UPDATE %s SET folder_id = $1, updated_at = $2
		WHERE id = $3 AND folder_id != $1 AND is_draft = false
	`, s.opts.table)

	result, err := s.db.ExecContext(ctx, query, store.FolderTrash, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("move to trash: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}

	return nil
}

func (s *Store) HardDelete(ctx context.Context, id string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`DELETE FROM %s WHERE id = $1 AND is_draft = false`, s.opts.table)
	result, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("hard delete: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}

	return nil
}

func (s *Store) Restore(ctx context.Context, id string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	// Read the message to determine restore folder based on ownership
	getQuery := fmt.Sprintf(`SELECT owner_id, sender_id FROM %s WHERE id = $1 AND folder_id = $2 AND is_draft = false`, s.opts.table)
	var ownerID, senderID string
	err := s.db.QueryRowContext(ctx, getQuery, id, store.FolderTrash).Scan(&ownerID, &senderID)
	if err != nil {
		return store.ErrNotFound
	}

	folderID := store.FolderInbox
	if store.IsSentByOwner(ownerID, senderID) {
		folderID = store.FolderSent
	}

	query := fmt.Sprintf(`
		UPDATE %s SET folder_id = $1, updated_at = $2
		WHERE id = $3 AND folder_id = $4
	`, s.opts.table)

	result, err := s.db.ExecContext(ctx, query, folderID, time.Now().UTC(), id, store.FolderTrash)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}

	return nil
}

func (s *Store) CreateMessage(ctx context.Context, data store.MessageData) (store.Message, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()
	id := uuid.New().String()

	metadataJSON, err := json.Marshal(data.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}

	attachmentsJSON, err := s.marshalAttachments(data.Attachments)
	if err != nil {
		return nil, fmt.Errorf("marshal attachments: %w", err)
	}

	query := fmt.Sprintf(`
		INSERT INTO %s (id, owner_id, sender_id, subject, body, metadata, status, folder_id,
		                recipient_ids, tags, attachments, is_draft, thread_id, reply_to_id,
		                created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id
	`, s.opts.table)

	var returnedID string
	err = s.db.QueryRowContext(ctx, query,
		id, data.OwnerID, data.SenderID, data.Subject, data.Body, metadataJSON,
		data.Status, data.FolderID, pq.Array(data.RecipientIDs), pq.Array(data.Tags),
		attachmentsJSON, false, data.ThreadID, data.ReplyToID, now, now,
	).Scan(&returnedID)
	if err != nil {
		return nil, fmt.Errorf("insert message: %w", err)
	}

	return &message{
		id:           returnedID,
		ownerID:      data.OwnerID,
		senderID:     data.SenderID,
		recipientIDs: data.RecipientIDs,
		subject:      data.Subject,
		body:         data.Body,
		metadata:     data.Metadata,
		status:       data.Status,
		folderID:     data.FolderID,
		tags:         data.Tags,
		attachments:  data.Attachments,
		threadID:     data.ThreadID,
		replyToID:    data.ReplyToID,
		createdAt:    now,
		updatedAt:    now,
	}, nil
}

func (s *Store) CreateMessages(ctx context.Context, data []store.MessageData) ([]store.Message, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	// Use a transaction for atomic batch insert
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	messages := make([]store.Message, 0, len(data))

	for _, d := range data {
		id := uuid.New().String()

		metadataJSON, err := json.Marshal(d.Metadata)
		if err != nil {
			return nil, fmt.Errorf("marshal metadata: %w", err)
		}

		attachmentsJSON, err := s.marshalAttachments(d.Attachments)
		if err != nil {
			return nil, fmt.Errorf("marshal attachments: %w", err)
		}

		query := fmt.Sprintf(`
			INSERT INTO %s (id, owner_id, sender_id, subject, body, metadata, status, folder_id,
			                recipient_ids, tags, attachments, is_draft, thread_id, reply_to_id,
			                created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		`, s.opts.table)

		_, err = tx.ExecContext(ctx, query,
			id, d.OwnerID, d.SenderID, d.Subject, d.Body, metadataJSON,
			d.Status, d.FolderID, pq.Array(d.RecipientIDs), pq.Array(d.Tags),
			attachmentsJSON, false, d.ThreadID, d.ReplyToID, now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert message: %w", err)
		}

		messages = append(messages, &message{
			id:           id,
			ownerID:      d.OwnerID,
			senderID:     d.SenderID,
			recipientIDs: d.RecipientIDs,
			subject:      d.Subject,
			body:         d.Body,
			metadata:     d.Metadata,
			status:       d.Status,
			folderID:     d.FolderID,
			tags:         d.Tags,
			attachments:  d.Attachments,
			threadID:     d.ThreadID,
			replyToID:    d.ReplyToID,
			createdAt:    now,
			updatedAt:    now,
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return messages, nil
}

// CreateMessageIdempotent atomically creates a message or returns existing.
func (s *Store) CreateMessageIdempotent(ctx context.Context, data store.MessageData, idempotencyKey string) (store.Message, bool, error) {
	if err := s.checkConnected(); err != nil {
		return nil, false, err
	}

	if idempotencyKey == "" {
		return nil, false, store.ErrInvalidIdempotencyKey
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()
	id := uuid.New().String()

	metadataJSON, err := json.Marshal(data.Metadata)
	if err != nil {
		return nil, false, fmt.Errorf("marshal metadata: %w", err)
	}

	attachmentsJSON, err := s.marshalAttachments(data.Attachments)
	if err != nil {
		return nil, false, fmt.Errorf("marshal attachments: %w", err)
	}

	// Try to insert, ignore conflict
	insertQuery := fmt.Sprintf(`
		INSERT INTO %s (id, owner_id, sender_id, subject, body, metadata, status, folder_id,
		                recipient_ids, tags, attachments, is_draft, idempotency_key,
		                thread_id, reply_to_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (owner_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id, created_at
	`, s.opts.table)

	var returnedID string
	var createdAt time.Time
	err = s.db.QueryRowContext(ctx, insertQuery,
		id, data.OwnerID, data.SenderID, data.Subject, data.Body, metadataJSON,
		data.Status, data.FolderID, pq.Array(data.RecipientIDs), pq.Array(data.Tags),
		attachmentsJSON, false, idempotencyKey, data.ThreadID, data.ReplyToID,
		now, now,
	).Scan(&returnedID, &createdAt)

	if err == sql.ErrNoRows {
		// Conflict occurred - fetch existing
		selectQuery := fmt.Sprintf(`
			SELECT id, owner_id, sender_id, subject, body, metadata, status, folder_id,
			       is_read, read_at, recipient_ids, tags, attachments, is_deleted, is_draft,
			       idempotency_key, thread_id, reply_to_id,
			       created_at, updated_at
			FROM %s
			WHERE owner_id = $1 AND idempotency_key = $2
		`, s.opts.table)

		msg, err := s.scanMessage(s.db.QueryRowContext(ctx, selectQuery, data.OwnerID, idempotencyKey))
		if err != nil {
			return nil, false, fmt.Errorf("fetch existing: %w", err)
		}
		return msg, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("insert idempotent: %w", err)
	}

	// New message was created
	return &message{
		id:             returnedID,
		ownerID:        data.OwnerID,
		senderID:       data.SenderID,
		recipientIDs:   data.RecipientIDs,
		subject:        data.Subject,
		body:           data.Body,
		metadata:       data.Metadata,
		status:         data.Status,
		folderID:       data.FolderID,
		tags:           data.Tags,
		attachments:    data.Attachments,
		idempotencyKey: idempotencyKey,
		threadID:       data.ThreadID,
		replyToID:      data.ReplyToID,
		createdAt:      createdAt,
		updatedAt:      now,
	}, true, nil
}

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

// DeleteExpiredTrash atomically deletes all messages in trash older than cutoff.
func (s *Store) DeleteExpiredTrash(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`
		DELETE FROM %s
		WHERE folder_id = $1 AND updated_at < $2
	`, s.opts.table)

	result, err := s.db.ExecContext(ctx, query, store.FolderTrash, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired trash: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}

	return count, nil
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
