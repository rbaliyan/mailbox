package memory

import (
	"context"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// MarkRead sets the read status of a message.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) MarkRead(ctx context.Context, id string, read bool) error {
	if err := s.checkConnected(); err != nil {
		return err
	}
	if id == "" {
		return store.ErrInvalidID
	}

	// Acquire per-message lock to prevent concurrent mutation races
	lock := s.getMsgLock(id)
	lock.Lock()
	defer lock.Unlock()

	v, ok := s.messages.Load(id)
	if !ok {
		return store.ErrNotFound
	}

	orig := v.(*message)
	if orig.isDraft {
		return store.ErrNotFound
	}

	// Copy-on-write: clone, modify, store (now atomic within lock)
	m := orig.clone()
	m.isRead = read
	m.updatedAt = time.Now().UTC()
	if read {
		now := time.Now().UTC()
		m.readAt = &now
	} else {
		m.readAt = nil
	}
	s.messages.Store(id, m)

	return nil
}

// MarkAllRead marks all unread non-draft messages in a folder as read.
// Uses per-message locking for each update to prevent concurrent mutation races.
func (s *Store) MarkAllRead(ctx context.Context, ownerID, folderID string) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}

	var count int64
	now := time.Now().UTC()

	s.messages.Range(func(key, value any) bool {
		m := value.(*message)
		if m.isDraft || m.ownerID != ownerID || m.folderID != folderID || m.isRead {
			return true
		}

		lock := s.getMsgLock(key.(string))
		lock.Lock()
		v, ok := s.messages.Load(key)
		if ok {
			orig := v.(*message)
			if !orig.isDraft && !orig.isRead && orig.ownerID == ownerID && orig.folderID == folderID {
				clone := orig.clone()
				clone.isRead = true
				clone.readAt = &now
				clone.updatedAt = now
				s.messages.Store(key, clone)
				count++
			}
		}
		lock.Unlock()
		return true
	})

	return count, nil
}

// MoveToFolder moves a message to a different folder.
// When called with store.FromFolder, the move is conditional: it succeeds only
// if the message is currently in the specified source folder.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) MoveToFolder(ctx context.Context, id string, folderID string, opts ...store.MoveOption) error {
	if err := s.checkConnected(); err != nil {
		return err
	}
	if id == "" {
		return store.ErrInvalidID
	}

	mo := store.ApplyMoveOptions(opts)
	if from := mo.FromFolderID(); from != "" && !store.IsValidFolderID(from) {
		return store.ErrInvalidFolderID
	}

	// Acquire per-message lock to prevent concurrent mutation races
	lock := s.getMsgLock(id)
	lock.Lock()
	defer lock.Unlock()

	v, ok := s.messages.Load(id)
	if !ok {
		return store.ErrNotFound
	}

	orig := v.(*message)
	if orig.isDraft {
		return store.ErrNotFound
	}

	// Conditional move: check source folder within lock (atomic CAS).
	if from := mo.FromFolderID(); from != "" && orig.folderID != from {
		return store.ErrFolderMismatch
	}

	// Copy-on-write: clone, modify, store (now atomic within lock)
	m := orig.clone()
	m.folderID = folderID
	m.updatedAt = time.Now().UTC()
	s.messages.Store(id, m)
	return nil
}

// AddTag adds a tag to a message.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) AddTag(ctx context.Context, id string, tagID string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}
	if id == "" {
		return store.ErrInvalidID
	}

	// Acquire per-message lock to prevent concurrent mutation races
	lock := s.getMsgLock(id)
	lock.Lock()
	defer lock.Unlock()

	v, ok := s.messages.Load(id)
	if !ok {
		return store.ErrNotFound
	}

	orig := v.(*message)
	if orig.isDraft {
		return store.ErrNotFound
	}

	// Check if tag already exists
	for _, t := range orig.tags {
		if t == tagID {
			return nil
		}
	}

	// Copy-on-write: clone, modify, store (now atomic within lock)
	m := orig.clone()
	m.tags = append(m.tags, tagID)
	m.updatedAt = time.Now().UTC()
	s.messages.Store(id, m)
	return nil
}

// RemoveTag removes a tag from a message.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) RemoveTag(ctx context.Context, id string, tagID string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}
	if id == "" {
		return store.ErrInvalidID
	}

	// Acquire per-message lock to prevent concurrent mutation races
	lock := s.getMsgLock(id)
	lock.Lock()
	defer lock.Unlock()

	v, ok := s.messages.Load(id)
	if !ok {
		return store.ErrNotFound
	}

	orig := v.(*message)
	if orig.isDraft {
		return store.ErrNotFound
	}

	// Find tag index
	tagIndex := -1
	for i, t := range orig.tags {
		if t == tagID {
			tagIndex = i
			break
		}
	}
	if tagIndex == -1 {
		return nil // Tag not found, nothing to do
	}

	// Copy-on-write: clone, modify, store (now atomic within lock)
	m := orig.clone()
	m.tags = append(m.tags[:tagIndex], m.tags[tagIndex+1:]...)
	m.updatedAt = time.Now().UTC()
	s.messages.Store(id, m)
	return nil
}

// Delete soft-deletes a message.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) Delete(ctx context.Context, id string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}
	if id == "" {
		return store.ErrInvalidID
	}

	// Acquire per-message lock to prevent concurrent mutation races
	lock := s.getMsgLock(id)
	lock.Lock()
	defer lock.Unlock()

	v, ok := s.messages.Load(id)
	if !ok {
		return store.ErrNotFound
	}

	orig := v.(*message)
	if orig.isDraft {
		return store.ErrNotFound
	}

	// Copy-on-write: clone, modify, store (now atomic within lock)
	m := orig.clone()
	m.folderID = store.FolderTrash
	m.updatedAt = time.Now().UTC()
	s.messages.Store(id, m)
	return nil
}

// HardDelete permanently removes a message.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) HardDelete(ctx context.Context, id string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}
	if id == "" {
		return store.ErrInvalidID
	}

	// Acquire per-message lock to prevent concurrent mutation races
	lock := s.getMsgLock(id)
	lock.Lock()
	defer lock.Unlock()

	v, ok := s.messages.Load(id)
	if !ok {
		return store.ErrNotFound
	}

	m := v.(*message)
	if m.isDraft {
		return store.ErrNotFound
	}

	s.messages.Delete(id)
	return nil
}

// Restore restores a soft-deleted message from trash.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) Restore(ctx context.Context, id string) error {
	if err := s.checkConnected(); err != nil {
		return err
	}
	if id == "" {
		return store.ErrInvalidID
	}

	// Acquire per-message lock to prevent concurrent mutation races
	lock := s.getMsgLock(id)
	lock.Lock()
	defer lock.Unlock()

	v, ok := s.messages.Load(id)
	if !ok {
		return store.ErrNotFound
	}

	orig := v.(*message)
	if orig.isDraft || orig.folderID != store.FolderTrash {
		return store.ErrNotFound
	}

	// Copy-on-write: clone, modify, store (now atomic within lock)
	m := orig.clone()
	// Restore to appropriate folder based on sender
	if store.IsSentByOwner(m.ownerID, m.senderID) {
		m.folderID = store.FolderSent
	} else {
		m.folderID = store.FolderInbox
	}
	m.updatedAt = time.Now().UTC()
	s.messages.Store(id, m)
	return nil
}

// --- BulkUpdater implementation ---

var _ store.BulkUpdater = (*Store)(nil)

func (s *Store) iterateFiltered(ownerID string, filters []store.Filter, fn func(m *message)) int64 {
	var count int64
	now := time.Now().UTC()
	s.messages.Range(func(key, value any) bool {
		m := value.(*message)
		if m.isDraft || m.ownerID != ownerID {
			return true
		}
		if m.availableAt != nil && m.availableAt.After(now) {
			return true
		}
		if !matchesFilters(m, filters) {
			return true
		}
		lock := s.getMsgLock(key.(string))
		lock.Lock()
		fn(m)
		m.updatedAt = now
		lock.Unlock()
		count++
		return true
	})
	return count
}

func (s *Store) MarkReadByFilter(_ context.Context, ownerID string, filters []store.Filter, read bool) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	return s.iterateFiltered(ownerID, filters, func(m *message) {
		m.isRead = read
		if read {
			m.readAt = &now
		} else {
			m.readAt = nil
		}
	}), nil
}

func (s *Store) MoveByFilter(_ context.Context, ownerID string, filters []store.Filter, folderID string) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	return s.iterateFiltered(ownerID, filters, func(m *message) {
		m.folderID = folderID
	}), nil
}

func (s *Store) DeleteByFilter(_ context.Context, ownerID string, filters []store.Filter) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	return s.iterateFiltered(ownerID, filters, func(m *message) {
		m.folderID = store.FolderTrash
	}), nil
}

func (s *Store) AddTagByFilter(_ context.Context, ownerID string, filters []store.Filter, tagID string) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	return s.iterateFiltered(ownerID, filters, func(m *message) {
		for _, t := range m.tags {
			if t == tagID {
				return
			}
		}
		m.tags = append(m.tags, tagID)
	}), nil
}

func (s *Store) RemoveTagByFilter(_ context.Context, ownerID string, filters []store.Filter, tagID string) (int64, error) {
	if err := s.checkConnected(); err != nil {
		return 0, err
	}
	return s.iterateFiltered(ownerID, filters, func(m *message) {
		for i, t := range m.tags {
			if t == tagID {
				m.tags = append(m.tags[:i], m.tags[i+1:]...)
				return
			}
		}
	}), nil
}
