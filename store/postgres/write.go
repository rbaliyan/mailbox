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
