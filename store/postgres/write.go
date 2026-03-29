package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
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

func (s *Store) MoveToFolder(ctx context.Context, id string, folderID string, opts ...store.MoveOption) error {
	if err := s.checkConnected(); err != nil {
		return err
	}

	if _, err := uuid.Parse(id); err != nil {
		return store.ErrInvalidID
	}

	if !store.IsValidFolderID(folderID) {
		return store.ErrInvalidFolderID
	}

	mo := store.ApplyMoveOptions(opts)
	if from := mo.FromFolderID(); from != "" && !store.IsValidFolderID(from) {
		return store.ErrInvalidFolderID
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	var query string
	var args []any
	if from := mo.FromFolderID(); from != "" {
		query = fmt.Sprintf(`
			UPDATE %s SET folder_id = $1, updated_at = $2
			WHERE id = $3 AND is_draft = false AND folder_id = $4
		`, s.opts.table)
		args = []any{folderID, time.Now().UTC(), id, from}
	} else {
		query = fmt.Sprintf(`
			UPDATE %s SET folder_id = $1, updated_at = $2
			WHERE id = $3 AND is_draft = false
		`, s.opts.table)
		args = []any{folderID, time.Now().UTC(), id}
	}

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("move to folder: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		// If conditional move, distinguish not-found from folder mismatch.
		// Note: between the failed update and this existence check, the message
		// could be deleted by another process. In that narrow window we return
		// ErrNotFound instead of ErrFolderMismatch — this is benign because the
		// caller would get ErrNotFound on the next attempt anyway.
		if mo.FromFolderID() != "" {
			existsQuery := fmt.Sprintf(`SELECT 1 FROM %s WHERE id = $1 AND is_draft = false LIMIT 1`, s.opts.table)
			var exists int
			if err := s.db.QueryRowContext(ctx, existsQuery, id).Scan(&exists); err == nil {
				return store.ErrFolderMismatch
			}
		}
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

// MarkAllRead marks all unread non-draft messages in a folder as read.
// Uses a single UPDATE for efficiency.
func (s *Store) MarkAllRead(ctx context.Context, ownerID, folderID string) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	now := time.Now().UTC()
	query := fmt.Sprintf(`UPDATE %s SET is_read = true, read_at = $1, updated_at = $1
		WHERE owner_id = $2 AND folder_id = $3 AND is_read = false AND is_draft = false`, s.opts.table)

	result, err := s.db.ExecContext(ctx, query, now, ownerID, folderID)
	if err != nil {
		return 0, fmt.Errorf("mark all read: %w", err)
	}

	return result.RowsAffected()
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

// --- BulkUpdater implementation ---

var _ store.BulkUpdater = (*Store)(nil)

func (s *Store) bulkWhere(ownerID string, filters []store.Filter) (string, []any) {
	argIdx := 2 // $1 is ownerID
	where, args := s.buildWhereClause(filters)
	// Offset the filter args
	fullWhere := fmt.Sprintf("owner_id = $1 AND is_draft = false AND (available_at IS NULL OR available_at <= NOW())")
	if where != "1=1" {
		fullWhere += " AND " + where
	}
	_ = argIdx
	return fullWhere, append([]any{ownerID}, args...)
}

func (s *Store) MarkReadByFilter(ctx context.Context, ownerID string, filters []store.Filter, read bool) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	where, args := s.bulkWhere(ownerID, filters)
	n := len(args)
	var query string
	if read {
		query = fmt.Sprintf("UPDATE %s SET is_read = $%d, read_at = $%d, updated_at = $%d WHERE %s",
			s.opts.table, n+1, n+2, n+3, where)
		now := time.Now().UTC()
		args = append(args, true, now, now)
	} else {
		query = fmt.Sprintf("UPDATE %s SET is_read = $%d, read_at = NULL, updated_at = $%d WHERE %s",
			s.opts.table, n+1, n+2, where)
		args = append(args, false, time.Now().UTC())
	}

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("mark read by filter: %w", err)
	}
	count, _ := result.RowsAffected()
	return count, nil
}

func (s *Store) MoveByFilter(ctx context.Context, ownerID string, filters []store.Filter, folderID string) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	where, args := s.bulkWhere(ownerID, filters)
	n := len(args)
	query := fmt.Sprintf("UPDATE %s SET folder_id = $%d, updated_at = $%d WHERE %s",
		s.opts.table, n+1, n+2, where)
	args = append(args, folderID, time.Now().UTC())

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("move by filter: %w", err)
	}
	count, _ := result.RowsAffected()
	return count, nil
}

func (s *Store) DeleteByFilter(ctx context.Context, ownerID string, filters []store.Filter) (int64, error) {
	return s.MoveByFilter(ctx, ownerID, filters, store.FolderTrash)
}

func (s *Store) AddTagByFilter(ctx context.Context, ownerID string, filters []store.Filter, tagID string) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	where, args := s.bulkWhere(ownerID, filters)
	n := len(args)
	query := fmt.Sprintf("UPDATE %s SET tags = array_append(tags, $%d), updated_at = $%d WHERE %s AND NOT ($%d = ANY(tags))",
		s.opts.table, n+1, n+2, where, n+3)
	now := time.Now().UTC()
	args = append(args, tagID, now, tagID)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("add tag by filter: %w", err)
	}
	count, _ := result.RowsAffected()
	return count, nil
}

func (s *Store) RemoveTagByFilter(ctx context.Context, ownerID string, filters []store.Filter, tagID string) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.timeout)
	defer cancel()

	where, args := s.bulkWhere(ownerID, filters)
	n := len(args)
	query := fmt.Sprintf("UPDATE %s SET tags = array_remove(tags, $%d), updated_at = $%d WHERE %s",
		s.opts.table, n+1, n+2, where)
	args = append(args, tagID, time.Now().UTC())

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("remove tag by filter: %w", err)
	}
	count, _ := result.RowsAffected()
	return count, nil
}
