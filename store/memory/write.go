package memory

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// MarkRead sets the read status of a message.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) MarkRead(ctx context.Context, id string, read bool) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
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
	if atomic.LoadInt32(&s.connected) == 0 {
		return 0, store.ErrNotConnected
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
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) MoveToFolder(ctx context.Context, id string, folderID string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
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
	m.folderID = folderID
	m.updatedAt = time.Now().UTC()
	s.messages.Store(id, m)
	return nil
}

// AddTag adds a tag to a message.
// Uses per-message locking to prevent concurrent mutation races.
func (s *Store) AddTag(ctx context.Context, id string, tagID string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
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
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
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
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
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
func (s *Store) HardDelete(ctx context.Context, id string) error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}
	if id == "" {
		return store.ErrInvalidID
	}

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
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
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

