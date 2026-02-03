// Package postgres provides a PostgreSQL implementation of store.Store.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/rbaliyan/mailbox/store"
)

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

// =============================================================================
// Message Operations
// =============================================================================

func (s *Store) Get(ctx context.Context, id string) (store.Message, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

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
		WHERE id = $1 AND is_draft = false
	`, s.opts.table)

	msg, err := s.scanMessage(s.db.QueryRowContext(ctx, query, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get message: %w", err)
	}

	return msg, nil
}

func (s *Store) Find(ctx context.Context, filters []store.Filter, opts store.ListOptions) (*store.MessageList, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	// Apply defaults
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.SortBy == "" {
		opts.SortBy = "created_at"
		opts.SortOrder = store.SortDesc
	}

	// Build WHERE clause
	where, args := s.buildWhereClause(filters)
	where = where + " AND is_draft = false"

	// Count total
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, s.opts.table, where)
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}

	// Build ORDER BY
	sortOrder := "DESC"
	if opts.SortOrder == store.SortAsc {
		sortOrder = "ASC"
	}
	sortField := s.mapSortField(opts.SortBy)

	// Cursor-based pagination: use keyset filtering when StartAfter is provided
	if opts.StartAfter != "" {
		if _, err := uuid.Parse(opts.StartAfter); err != nil {
			return nil, store.ErrInvalidID
		}
		comp := "<"
		if opts.SortOrder == store.SortAsc {
			comp = ">"
		}
		where = where + fmt.Sprintf(` AND (%s, id) %s (SELECT %s, id FROM %s WHERE id = $%d)`,
			sortField, comp, sortField, s.opts.table, len(args)+1)
		args = append(args, opts.StartAfter)
	}

	// Query messages
	var query string
	if opts.StartAfter != "" {
		// Cursor-based: no OFFSET needed
		query = fmt.Sprintf(`
			SELECT id, owner_id, sender_id, subject, body, metadata, status, folder_id,
			       is_read, read_at, recipient_ids, tags, attachments, is_deleted, is_draft,
			       idempotency_key, thread_id, reply_to_id,
			       created_at, updated_at
			FROM %s
			WHERE %s
			ORDER BY %s %s
			LIMIT $%d
		`, s.opts.table, where, sortField, sortOrder, len(args)+1)
		args = append(args, opts.Limit+1)
	} else {
		// Offset-based
		query = fmt.Sprintf(`
			SELECT id, owner_id, sender_id, subject, body, metadata, status, folder_id,
			       is_read, read_at, recipient_ids, tags, attachments, is_deleted, is_draft,
			       idempotency_key, thread_id, reply_to_id,
			       created_at, updated_at
			FROM %s
			WHERE %s
			ORDER BY %s %s
			LIMIT $%d OFFSET $%d
		`, s.opts.table, where, sortField, sortOrder, len(args)+1, len(args)+2)
		args = append(args, opts.Limit+1, opts.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var messages []store.Message
	for rows.Next() {
		msg, err := s.scanMessageFromRows(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, msg)
	}

	hasMore := len(messages) > opts.Limit
	if hasMore {
		messages = messages[:opts.Limit]
	}

	var nextCursor string
	if hasMore && len(messages) > 0 {
		nextCursor = messages[len(messages)-1].GetID()
	}

	return &store.MessageList{
		Messages:   messages,
		Total:      total,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, nil
}

func (s *Store) Count(ctx context.Context, filters []store.Filter) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	where, args := s.buildWhereClause(filters)
	where = where + " AND is_draft = false"

	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, s.opts.table, where)
	var count int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}

	return count, nil
}

func (s *Store) Search(ctx context.Context, query store.SearchQuery) (*store.MessageList, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	// Apply defaults
	if query.Options.Limit <= 0 {
		query.Options.Limit = 20
	}

	// Build search conditions
	var conditions []string
	var args []any
	argIdx := 1

	// Text search using ILIKE
	if query.Query != "" {
		searchPattern := "%" + strings.ReplaceAll(query.Query, "%", "\\%") + "%"
		conditions = append(conditions, fmt.Sprintf("(subject ILIKE $%d OR body ILIKE $%d)", argIdx, argIdx))
		args = append(args, searchPattern)
		argIdx++
	}

	// Owner filter (required for security)
	if query.OwnerID != "" {
		conditions = append(conditions, fmt.Sprintf("owner_id = $%d", argIdx))
		args = append(args, query.OwnerID)
		argIdx++
	}

	// Apply additional filters
	for _, f := range query.Filters {
		cond, arg := s.filterToCondition(f, &argIdx)
		if cond != "" {
			conditions = append(conditions, cond)
			if arg != nil {
				args = append(args, arg)
			}
		}
	}

	// Tag filter
	if len(query.Tags) > 0 {
		conditions = append(conditions, fmt.Sprintf("tags @> $%d", argIdx))
		args = append(args, pq.Array(query.Tags))
		argIdx++
	}

	// Always exclude deleted and drafts
	conditions = append(conditions, "is_draft = false")

	where := strings.Join(conditions, " AND ")
	if where == "" {
		where = "1=1"
	}

	// Cursor-based pagination: use keyset filtering when StartAfter is provided
	if query.Options.StartAfter != "" {
		if _, err := uuid.Parse(query.Options.StartAfter); err != nil {
			return nil, store.ErrInvalidID
		}
		where = where + fmt.Sprintf(` AND (created_at, id) < (SELECT created_at, id FROM %s WHERE id = $%d)`,
			s.opts.table, argIdx)
		args = append(args, query.Options.StartAfter)
		argIdx++
	}

	// Count (without cursor filter)
	countWhere := strings.Join(conditions, " AND ")
	if countWhere == "" {
		countWhere = "1=1"
	}
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, s.opts.table, countWhere)
	countArgs := make([]any, len(args))
	copy(countArgs, args)
	// Remove cursor arg from count args if present
	if query.Options.StartAfter != "" {
		countArgs = countArgs[:len(countArgs)-1]
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count search: %w", err)
	}

	// Query
	var sqlQuery string
	if query.Options.StartAfter != "" {
		sqlQuery = fmt.Sprintf(`
			SELECT id, owner_id, sender_id, subject, body, metadata, status, folder_id,
			       is_read, read_at, recipient_ids, tags, attachments, is_deleted, is_draft,
			       idempotency_key, thread_id, reply_to_id,
			       created_at, updated_at
			FROM %s
			WHERE %s
			ORDER BY created_at DESC
			LIMIT $%d
		`, s.opts.table, where, argIdx)
		args = append(args, query.Options.Limit+1)
	} else {
		sqlQuery = fmt.Sprintf(`
			SELECT id, owner_id, sender_id, subject, body, metadata, status, folder_id,
			       is_read, read_at, recipient_ids, tags, attachments, is_deleted, is_draft,
			       idempotency_key, thread_id, reply_to_id,
			       created_at, updated_at
			FROM %s
			WHERE %s
			ORDER BY created_at DESC
			LIMIT $%d OFFSET $%d
		`, s.opts.table, where, argIdx, argIdx+1)
		args = append(args, query.Options.Limit+1, query.Options.Offset)
	}

	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var messages []store.Message
	for rows.Next() {
		msg, err := s.scanMessageFromRows(rows)
		if err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		messages = append(messages, msg)
	}

	hasMore := len(messages) > query.Options.Limit
	if hasMore {
		messages = messages[:query.Options.Limit]
	}

	var nextCursor string
	if hasMore && len(messages) > 0 {
		nextCursor = messages[len(messages)-1].GetID()
	}

	return &store.MessageList{
		Messages:   messages,
		Total:      total,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, nil
}

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

// =============================================================================
// Maintenance Operations
// =============================================================================

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

// =============================================================================
// Helper functions
// =============================================================================

func (s *Store) buildWhereClause(filters []store.Filter) (string, []any) {
	if len(filters) == 0 {
		return "1=1", nil
	}

	var conditions []string
	var args []any
	argIdx := 1

	for _, f := range filters {
		cond, arg := s.filterToCondition(f, &argIdx)
		if cond != "" {
			conditions = append(conditions, cond)
			if arg != nil {
				args = append(args, arg)
			}
		}
	}

	if len(conditions) == 0 {
		return "1=1", nil
	}

	return strings.Join(conditions, " AND "), args
}

func (s *Store) filterToCondition(f store.Filter, argIdx *int) (string, any) {
	key, ok := s.mapFilterKey(f.Key())
	if !ok {
		return "", nil
	}
	op := f.Operator()
	val := f.Value()

	switch op {
	case "eq", "":
		cond := fmt.Sprintf("%s = $%d", key, *argIdx)
		*argIdx++
		return cond, val
	case "ne":
		cond := fmt.Sprintf("%s != $%d", key, *argIdx)
		*argIdx++
		return cond, val
	case "gt":
		cond := fmt.Sprintf("%s > $%d", key, *argIdx)
		*argIdx++
		return cond, val
	case "gte":
		cond := fmt.Sprintf("%s >= $%d", key, *argIdx)
		*argIdx++
		return cond, val
	case "lt":
		cond := fmt.Sprintf("%s < $%d", key, *argIdx)
		*argIdx++
		return cond, val
	case "lte":
		cond := fmt.Sprintf("%s <= $%d", key, *argIdx)
		*argIdx++
		return cond, val
	case "in":
		cond := fmt.Sprintf("%s = ANY($%d)", key, *argIdx)
		*argIdx++
		return cond, pq.Array(val)
	case "nin":
		cond := fmt.Sprintf("NOT (%s = ANY($%d))", key, *argIdx)
		*argIdx++
		return cond, pq.Array(val)
	case "contains":
		// For array contains
		cond := fmt.Sprintf("$%d = ANY(%s)", *argIdx, key)
		*argIdx++
		return cond, val
	case "exists":
		// For array columns (tags, recipient_ids), check array length.
		// For scalar columns, check non-null and non-empty.
		if key == "tags" || key == "recipient_ids" {
			if val == true {
				return fmt.Sprintf("array_length(%s, 1) > 0", key), nil
			}
			return fmt.Sprintf("(array_length(%s, 1) IS NULL OR array_length(%s, 1) = 0)", key, key), nil
		}
		if val == true {
			return fmt.Sprintf("(%s IS NOT NULL AND %s != '')", key, key), nil
		}
		return fmt.Sprintf("(%s IS NULL OR %s = '')", key, key), nil
	default:
		return "", nil
	}
}

func (s *Store) mapFilterKey(key string) (string, bool) {
	switch key {
	case "id":
		return "id", true
	case "OwnerID", "owner_id":
		return "owner_id", true
	case "SenderID", "sender_id":
		return "sender_id", true
	case "RecipientIDs", "recipient_ids":
		return "recipient_ids", true
	case "Status", "status":
		return "status", true
	case "FolderID", "folder_id":
		return "folder_id", true
	case "IsRead", "is_read":
		return "is_read", true
	case "Tags", "tags":
		return "tags", true
	case "CreatedAt", "created_at":
		return "created_at", true
	case "UpdatedAt", "updated_at":
		return "updated_at", true
	case "ThreadID", "thread_id":
		return "thread_id", true
	case "ReplyToID", "reply_to_id":
		return "reply_to_id", true
	default:
		return "", false
	}
}

func (s *Store) mapSortField(field string) string {
	switch field {
	case "CreatedAt", "created_at":
		return "created_at"
	case "UpdatedAt", "updated_at":
		return "updated_at"
	case "Subject", "subject":
		return "subject"
	default:
		return "created_at"
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanMessage(row rowScanner) (*message, error) {
	var msg message
	var metadataJSON, attachmentsJSON []byte
	var readAt sql.NullTime
	var idempotencyKey, threadID, replyToID sql.NullString

	err := row.Scan(
		&msg.id, &msg.ownerID, &msg.senderID, &msg.subject, &msg.body,
		&metadataJSON, &msg.status, &msg.folderID, &msg.isRead, &readAt,
		pq.Array(&msg.recipientIDs), pq.Array(&msg.tags), &attachmentsJSON,
		&msg.isDeleted, &msg.isDraft, &idempotencyKey, &threadID, &replyToID,
		&msg.createdAt, &msg.updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if readAt.Valid {
		msg.readAt = &readAt.Time
	}
	if idempotencyKey.Valid {
		msg.idempotencyKey = idempotencyKey.String
	}
	if threadID.Valid {
		msg.threadID = threadID.String
	}
	if replyToID.Valid {
		msg.replyToID = replyToID.String
	}

	// Unmarshal JSON fields
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &msg.metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}

	if len(attachmentsJSON) > 0 {
		msg.attachments, err = s.unmarshalAttachments(attachmentsJSON)
		if err != nil {
			return nil, fmt.Errorf("unmarshal attachments: %w", err)
		}
	}

	return &msg, nil
}

func (s *Store) scanMessageFromRows(rows *sql.Rows) (*message, error) {
	return s.scanMessage(rows)
}

func (s *Store) marshalAttachments(attachments []store.Attachment) ([]byte, error) {
	if len(attachments) == 0 {
		return []byte("[]"), nil
	}

	docs := make([]attachmentDoc, len(attachments))
	for i, a := range attachments {
		docs[i] = attachmentDoc{
			ID:          a.GetID(),
			Filename:    a.GetFilename(),
			ContentType: a.GetContentType(),
			Size:        a.GetSize(),
			URI:         a.GetURI(),
			CreatedAt:   a.GetCreatedAt(),
		}
	}

	return json.Marshal(docs)
}

func (s *Store) unmarshalAttachments(data []byte) ([]store.Attachment, error) {
	var docs []attachmentDoc
	if err := json.Unmarshal(data, &docs); err != nil {
		return nil, err
	}

	attachments := make([]store.Attachment, len(docs))
	for i, d := range docs {
		attachments[i] = &attachment{
			id:          d.ID,
			filename:    d.Filename,
			contentType: d.ContentType,
			size:        d.Size,
			uri:         d.URI,
			createdAt:   d.CreatedAt,
		}
	}

	return attachments, nil
}

// =============================================================================
// Message type
// =============================================================================

type message struct {
	id               string
	ownerID          string
	senderID         string
	recipientIDs     []string
	subject          string
	body             string
	metadata         map[string]any
	status           store.MessageStatus
	isRead           bool
	readAt           *time.Time
	folderID         string
	tags             []string
	attachments      []store.Attachment
	isDeleted        bool
	isDraft          bool
	idempotencyKey   string
	threadID         string
	replyToID        string
	createdAt        time.Time
	updatedAt        time.Time
}

// Message getters
func (m *message) GetID() string                               { return m.id }
func (m *message) GetOwnerID() string                          { return m.ownerID }
func (m *message) GetSenderID() string                         { return m.senderID }
func (m *message) GetRecipientIDs() []string                   { return m.recipientIDs }
func (m *message) GetSubject() string                          { return m.subject }
func (m *message) GetBody() string                             { return m.body }
func (m *message) GetMetadata() map[string]any                 { return m.metadata }
func (m *message) GetStatus() store.MessageStatus              { return m.status }
func (m *message) GetIsRead() bool                             { return m.isRead }
func (m *message) GetReadAt() *time.Time                       { return m.readAt }
func (m *message) GetFolderID() string                         { return m.folderID }
func (m *message) GetTags() []string                           { return m.tags }
func (m *message) GetAttachments() []store.Attachment          { return m.attachments }
func (m *message) GetCreatedAt() time.Time                     { return m.createdAt }
func (m *message) GetUpdatedAt() time.Time                     { return m.updatedAt }
func (m *message) GetThreadID() string                         { return m.threadID }
func (m *message) GetReplyToID() string                        { return m.replyToID }
// Draft setters (fluent)
func (m *message) SetSubject(subject string) store.DraftMessage {
	m.subject = subject
	return m
}

func (m *message) SetBody(body string) store.DraftMessage {
	m.body = body
	return m
}

func (m *message) SetRecipients(recipientIDs ...string) store.DraftMessage {
	m.recipientIDs = recipientIDs
	return m
}

func (m *message) SetMetadata(key string, value any) store.DraftMessage {
	if m.metadata == nil {
		m.metadata = make(map[string]any)
	}
	m.metadata[key] = value
	return m
}

func (m *message) AddAttachment(attachment store.Attachment) store.DraftMessage {
	m.attachments = append(m.attachments, attachment)
	return m
}

// =============================================================================
// Attachment type
// =============================================================================

type attachmentDoc struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	URI         string    `json:"uri"`
	CreatedAt   time.Time `json:"created_at"`
}

type attachment struct {
	id          string
	filename    string
	contentType string
	size        int64
	uri         string
	createdAt   time.Time
}

func (a *attachment) GetID() string          { return a.id }
func (a *attachment) GetFilename() string    { return a.filename }
func (a *attachment) GetContentType() string { return a.contentType }
func (a *attachment) GetSize() int64         { return a.size }
func (a *attachment) GetURI() string         { return a.uri }
func (a *attachment) GetCreatedAt() time.Time { return a.createdAt }

// Compile-time checks
var _ store.Store = (*Store)(nil)
var _ store.Message = (*message)(nil)
var _ store.DraftMessage = (*message)(nil)
var _ store.Attachment = (*attachment)(nil)
