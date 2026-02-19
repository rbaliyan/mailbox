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

func (s *Store) CreateMessage(ctx context.Context, data store.MessageData) (store.Message, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()
	id := uuid.New().String()

	headersJSON, err := json.Marshal(data.Headers)
	if err != nil {
		return nil, fmt.Errorf("marshal headers: %w", err)
	}

	metadataJSON, err := json.Marshal(data.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}

	attachmentsJSON, err := s.marshalAttachments(data.Attachments)
	if err != nil {
		return nil, fmt.Errorf("marshal attachments: %w", err)
	}

	query := fmt.Sprintf(`
		INSERT INTO %s (id, owner_id, sender_id, subject, body, headers, metadata, status, folder_id,
		                recipient_ids, tags, attachments, is_draft, thread_id, reply_to_id,
		                created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id
	`, s.opts.table)

	var returnedID string
	err = s.db.QueryRowContext(ctx, query,
		id, data.OwnerID, data.SenderID, data.Subject, data.Body, headersJSON, metadataJSON,
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
		headers:      data.Headers,
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
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	messages := make([]store.Message, 0, len(data))

	for _, d := range data {
		id := uuid.New().String()

		headersJSON, err := json.Marshal(d.Headers)
		if err != nil {
			return nil, fmt.Errorf("marshal headers: %w", err)
		}

		metadataJSON, err := json.Marshal(d.Metadata)
		if err != nil {
			return nil, fmt.Errorf("marshal metadata: %w", err)
		}

		attachmentsJSON, err := s.marshalAttachments(d.Attachments)
		if err != nil {
			return nil, fmt.Errorf("marshal attachments: %w", err)
		}

		query := fmt.Sprintf(`
			INSERT INTO %s (id, owner_id, sender_id, subject, body, headers, metadata, status, folder_id,
			                recipient_ids, tags, attachments, is_draft, thread_id, reply_to_id,
			                created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		`, s.opts.table)

		_, err = tx.ExecContext(ctx, query,
			id, d.OwnerID, d.SenderID, d.Subject, d.Body, headersJSON, metadataJSON,
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
			headers:      d.Headers,
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

	headersJSON, err := json.Marshal(data.Headers)
	if err != nil {
		return nil, false, fmt.Errorf("marshal headers: %w", err)
	}

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
		INSERT INTO %s (id, owner_id, sender_id, subject, body, headers, metadata, status, folder_id,
		                recipient_ids, tags, attachments, is_draft, idempotency_key,
		                thread_id, reply_to_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (owner_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id, created_at
	`, s.opts.table)

	var returnedID string
	var createdAt time.Time
	err = s.db.QueryRowContext(ctx, insertQuery,
		id, data.OwnerID, data.SenderID, data.Subject, data.Body, headersJSON, metadataJSON,
		data.Status, data.FolderID, pq.Array(data.RecipientIDs), pq.Array(data.Tags),
		attachmentsJSON, false, idempotencyKey, data.ThreadID, data.ReplyToID,
		now, now,
	).Scan(&returnedID, &createdAt)

	if err == sql.ErrNoRows {
		// Conflict occurred - fetch existing
		selectQuery := fmt.Sprintf(`
			SELECT %s
			FROM %s
			WHERE owner_id = $1 AND idempotency_key = $2
		`, messageColumns, s.opts.table)

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
		headers:        data.Headers,
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
