package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/rbaliyan/mailbox/store"
)

// messageColumns is the canonical SELECT column list for scanning messages.
// It must match the field order expected by scanMessage / scanMessageFromRows.
const messageColumns = `id, owner_id, sender_id, subject, body, metadata, status, folder_id,
       is_read, read_at, recipient_ids, tags, attachments, is_deleted, is_draft,
       idempotency_key, thread_id, reply_to_id, created_at, updated_at`

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
		SELECT %s
		FROM %s
		WHERE id = $1 AND is_draft = false
	`, messageColumns, s.opts.table)

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
			SELECT %s
			FROM %s
			WHERE %s
			ORDER BY %s %s
			LIMIT $%d
		`, messageColumns, s.opts.table, where, sortField, sortOrder, len(args)+1)
		args = append(args, opts.Limit+1)
	} else {
		// Offset-based
		query = fmt.Sprintf(`
			SELECT %s
			FROM %s
			WHERE %s
			ORDER BY %s %s
			LIMIT $%d OFFSET $%d
		`, messageColumns, s.opts.table, where, sortField, sortOrder, len(args)+1, len(args)+2)
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
			SELECT %s
			FROM %s
			WHERE %s
			ORDER BY created_at DESC
			LIMIT $%d
		`, messageColumns, s.opts.table, where, argIdx)
		args = append(args, query.Options.Limit+1)
	} else {
		sqlQuery = fmt.Sprintf(`
			SELECT %s
			FROM %s
			WHERE %s
			ORDER BY created_at DESC
			LIMIT $%d OFFSET $%d
		`, messageColumns, s.opts.table, where, argIdx, argIdx+1)
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
	key, ok := store.MessageFieldKey(f.Key())
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
