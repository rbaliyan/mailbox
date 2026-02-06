// Package postgres provides a PostgreSQL implementation of store.Store.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/rbaliyan/mailbox/store"
)

// Compile-time check
var _ store.Store = (*Store)(nil)

// Store implements store.Store using PostgreSQL.
type Store struct {
	db        *sqlx.DB
	opts      *options
	connected int32
	logger    *slog.Logger
}

// New creates a new PostgreSQL store with the provided database connection.
// Call Connect() to initialize the schema and indexes.
func New(db *sqlx.DB, opts ...Option) *Store {
	o := newOptions(opts...)
	return &Store{
		db:     db,
		opts:   o,
		logger: o.logger,
	}
}

// NewFromDB creates a new PostgreSQL store from a standard sql.DB connection.
// This wraps the sql.DB with sqlx for enhanced functionality.
func NewFromDB(db *sql.DB, opts ...Option) *Store {
	return New(sqlx.NewDb(db, "postgres"), opts...)
}

// Connect initializes the schema and indexes.
func (s *Store) Connect(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.connected, 0, 1) {
		return store.ErrAlreadyConnected
	}

	if s.db == nil {
		atomic.StoreInt32(&s.connected, 0)
		return fmt.Errorf("postgres: db is required")
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	if err := s.db.PingContext(ctx); err != nil {
		atomic.StoreInt32(&s.connected, 0)
		return fmt.Errorf("postgres ping: %w", err)
	}

	if err := s.ensureSchema(ctx); err != nil {
		atomic.StoreInt32(&s.connected, 0)
		return fmt.Errorf("ensure schema: %w", err)
	}

	s.logger.Info("connected to PostgreSQL", "table", s.opts.table)
	return nil
}

// Close marks the store as disconnected.
// The caller is responsible for closing the database connection.
func (s *Store) Close(ctx context.Context) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil
	}
	atomic.StoreInt32(&s.connected, 0)
	return nil
}

// ensureSchema creates the required table and indexes.
func (s *Store) ensureSchema(ctx context.Context) error {
	// Create table
	createTable := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			owner_id VARCHAR(255) NOT NULL,
			sender_id VARCHAR(255) NOT NULL,
			subject TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			metadata JSONB DEFAULT '{}',
			status VARCHAR(50) NOT NULL DEFAULT 'draft',
			folder_id VARCHAR(255) NOT NULL DEFAULT '__inbox',
			is_read BOOLEAN DEFAULT FALSE,
			read_at TIMESTAMPTZ,
			recipient_ids TEXT[] NOT NULL DEFAULT '{}',
			tags TEXT[] NOT NULL DEFAULT '{}',
			attachments JSONB DEFAULT '[]',
			is_deleted BOOLEAN DEFAULT FALSE,
			is_draft BOOLEAN DEFAULT FALSE,
			idempotency_key VARCHAR(255),
			thread_id VARCHAR(255),
			reply_to_id VARCHAR(255),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, s.opts.table)

	if _, err := s.db.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Create indexes
	indexes := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_owner ON %s(owner_id)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_sender ON %s(sender_id)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_folder ON %s(folder_id)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_status ON %s(status)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_created ON %s(created_at DESC)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_updated ON %s(updated_at DESC)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_is_read ON %s(is_read)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_is_draft ON %s(is_draft)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_tags ON %s USING GIN(tags)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_recipients ON %s USING GIN(recipient_ids)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_thread ON %s(thread_id) WHERE thread_id IS NOT NULL`, s.opts.table, s.opts.table),
		// Compound indexes for common queries
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_owner_folder ON %s(owner_id, folder_id, created_at DESC)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_owner_draft ON %s(owner_id, is_draft, created_at DESC)`, s.opts.table, s.opts.table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_folder_updated ON %s(folder_id, updated_at)`, s.opts.table, s.opts.table),
	}

	for _, idx := range indexes {
		if _, err := s.db.ExecContext(ctx, idx); err != nil {
			s.logger.Warn("failed to create index", "error", err, "sql", idx)
		}
	}

	// Create unique index for idempotency (with partial filter)
	idempotencyIdx := fmt.Sprintf(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_%s_idempotency
		ON %s(owner_id, idempotency_key)
		WHERE idempotency_key IS NOT NULL
	`, s.opts.table, s.opts.table)
	if _, err := s.db.ExecContext(ctx, idempotencyIdx); err != nil {
		s.logger.Warn("failed to create idempotency index", "error", err)
	}

	return nil
}

// checkConnected returns error if not connected.
func (s *Store) checkConnected() error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}
	return nil
}

// =============================================================================
// Draft Operations
// =============================================================================

func (s *Store) NewDraft(ownerID string) store.DraftMessage {
	return &message{
		ownerID:  ownerID,
		senderID: ownerID,
		isDraft:  true,
		status:   store.MessageStatusDraft,
		folderID: store.FolderDrafts,
	}
}

func (s *Store) GetDraft(ctx context.Context, id string) (store.DraftMessage, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	// Validate UUID
	if _, err := uuid.Parse(id); err != nil {
		return nil, store.ErrInvalidID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`
		SELECT id, owner_id, sender_id, subject, body, metadata, status, folder_id,
		       is_read, read_at, recipient_ids, tags, attachments, is_deleted, is_draft,
		       idempotency_key, thread_id, reply_to_id,
		       created_at, updated_at
		FROM %s
		WHERE id = $1 AND is_draft = true
	`, s.opts.table)

	msg, err := s.scanMessage(s.db.QueryRowContext(ctx, query, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get draft: %w", err)
	}

	return msg, nil
}

func (s *Store) SaveDraft(ctx context.Context, draft store.DraftMessage) (store.DraftMessage, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	msg, ok := draft.(*message)
	if !ok {
		// Convert from interface
		msg = &message{
			id:           draft.GetID(),
			ownerID:      draft.GetOwnerID(),
			senderID:     draft.GetSenderID(),
			recipientIDs: draft.GetRecipientIDs(),
			subject:      draft.GetSubject(),
			body:         draft.GetBody(),
			metadata:     draft.GetMetadata(),
			status:       store.MessageStatusDraft,
			folderID:     store.FolderDrafts,
			attachments:  draft.GetAttachments(),
			isDraft:      true,
		}
	}

	now := time.Now().UTC()
	msg.updatedAt = now
	msg.isDraft = true
	msg.status = store.MessageStatusDraft

	// Marshal JSON fields
	metadataJSON, err := json.Marshal(msg.metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}

	attachmentsJSON, err := s.marshalAttachments(msg.attachments)
	if err != nil {
		return nil, fmt.Errorf("marshal attachments: %w", err)
	}

	if msg.id == "" {
		// Insert new draft
		msg.id = uuid.New().String()
		msg.createdAt = now

		query := fmt.Sprintf(`
			INSERT INTO %s (id, owner_id, sender_id, subject, body, metadata, status, folder_id,
			                recipient_ids, tags, attachments, is_draft, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING id
		`, s.opts.table)

		err := s.db.QueryRowContext(ctx, query,
			msg.id, msg.ownerID, msg.senderID, msg.subject, msg.body, metadataJSON,
			msg.status, msg.folderID, pq.Array(msg.recipientIDs), pq.Array(msg.tags),
			attachmentsJSON, true, msg.createdAt, msg.updatedAt,
		).Scan(&msg.id)
		if err != nil {
			return nil, fmt.Errorf("insert draft: %w", err)
		}
	} else {
		// Update existing draft
		query := fmt.Sprintf(`
			UPDATE %s
			SET subject = $1, body = $2, metadata = $3, recipient_ids = $4,
			    attachments = $5, updated_at = $6
			WHERE id = $7 AND is_draft = true
			RETURNING id
		`, s.opts.table)

		var returnedID string
		err := s.db.QueryRowContext(ctx, query,
			msg.subject, msg.body, metadataJSON, pq.Array(msg.recipientIDs),
			attachmentsJSON, msg.updatedAt, msg.id,
		).Scan(&returnedID)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, store.ErrNotFound
			}
			return nil, fmt.Errorf("update draft: %w", err)
		}
	}

	return msg, nil
}

func (s *Store) DeleteDraft(ctx context.Context, id string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	query := fmt.Sprintf(`DELETE FROM %s WHERE id = $1 AND is_draft = true`, s.opts.table)
	result, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete draft: %w", err)
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

func (s *Store) ListDrafts(ctx context.Context, ownerID string, opts store.ListOptions) (*store.DraftList, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	// Apply defaults
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	// Count total
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE owner_id = $1 AND is_draft = true`, s.opts.table)
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, ownerID).Scan(&total); err != nil {
		return nil, fmt.Errorf("count drafts: %w", err)
	}

	// Query drafts
	query := fmt.Sprintf(`
		SELECT id, owner_id, sender_id, subject, body, metadata, status, folder_id,
		       is_read, read_at, recipient_ids, tags, attachments, is_deleted, is_draft,
		       idempotency_key, thread_id, reply_to_id,
		       created_at, updated_at
		FROM %s
		WHERE owner_id = $1 AND is_draft = true
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, s.opts.table)

	rows, err := s.db.QueryContext(ctx, query, ownerID, opts.Limit+1, opts.Offset)
	if err != nil {
		return nil, fmt.Errorf("query drafts: %w", err)
	}
	defer rows.Close()

	var drafts []store.DraftMessage
	for rows.Next() {
		msg, err := s.scanMessageFromRows(rows)
		if err != nil {
			return nil, fmt.Errorf("scan draft: %w", err)
		}
		drafts = append(drafts, msg)
	}

	hasMore := len(drafts) > opts.Limit
	if hasMore {
		drafts = drafts[:opts.Limit]
	}

	return &store.DraftList{
		Drafts:  drafts,
		Total:   total,
		HasMore: hasMore,
	}, nil
}
