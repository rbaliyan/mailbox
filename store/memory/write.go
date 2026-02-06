package memory

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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

// CreateMessage creates a new message from the given data.
func (s *Store) CreateMessage(ctx context.Context, data store.MessageData) (store.Message, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	now := time.Now().UTC()
	m := &message{
		id:        uuid.New().String(),
		ownerID:   data.OwnerID,
		senderID:  data.SenderID,
		subject:   data.Subject,
		body:      data.Body,
		status:    data.Status,
		folderID:  data.FolderID,
		threadID:  data.ThreadID,
		replyToID: data.ReplyToID,
		createdAt: now,
		updatedAt: now,
		isDraft:   false,
	}

	if data.RecipientIDs != nil {
		m.recipientIDs = make([]string, len(data.RecipientIDs))
		copy(m.recipientIDs, data.RecipientIDs)
	}
	if data.Tags != nil {
		m.tags = make([]string, len(data.Tags))
		copy(m.tags, data.Tags)
	}
	if data.Metadata != nil {
		m.metadata = make(map[string]any, len(data.Metadata))
		for k, v := range data.Metadata {
			m.metadata[k] = v
		}
	}
	if data.Attachments != nil {
		m.attachments = make([]store.Attachment, len(data.Attachments))
		copy(m.attachments, data.Attachments)
	}

	s.messages.Store(m.id, m)
	return m.clone(), nil
}

// CreateMessages creates multiple messages atomically.
// For the memory store, this uses a simple loop since sync.Map operations
// are already atomic per-key. In production stores, this should use
// database transactions for true atomicity.
func (s *Store) CreateMessages(ctx context.Context, data []store.MessageData) ([]store.Message, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	// Create all messages - memory store doesn't have true transactions,
	// but each Store operation is atomic via sync.Map
	messages := make([]store.Message, len(data))
	for i, d := range data {
		msg, err := s.CreateMessage(ctx, d)
		if err != nil {
			// In a real implementation, this would rollback.
			// Memory store is for testing only.
			return nil, err
		}
		messages[i] = msg
	}
	return messages, nil
}

// CreateMessageIdempotent atomically creates a message or returns existing.
//
// Uses sync.Map.LoadOrStore for atomic check-and-create. This provides the
// same semantics as MongoDB's findOneAndUpdate with upsert or PostgreSQL's
// INSERT ON CONFLICT, but in memory.
//
// The idempotency index maps "ownerID:idempotencyKey" to message ID.
func (s *Store) CreateMessageIdempotent(ctx context.Context, data store.MessageData, idempotencyKey string) (store.Message, bool, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, false, store.ErrNotConnected
	}
	if idempotencyKey == "" {
		return nil, false, store.ErrInvalidIdempotencyKey
	}

	// Create the idempotency index key
	idxKey := data.OwnerID + ":" + idempotencyKey

	// Loop to handle the rare case where index exists but message was deleted.
	// Limited to 2 attempts to prevent infinite loops.
	var newMsgID string
	for attempt := 0; attempt < 2; attempt++ {
		// Generate a new message ID optimistically
		newMsgID = uuid.New().String()

		// Atomically try to store the idempotency mapping
		// If it already exists, LoadOrStore returns the existing value
		existingID, loaded := s.idempotencyIdx.LoadOrStore(idxKey, newMsgID)

		if !loaded {
			break // We won the race, proceed to create
		}

		// Message already exists, return it
		msgID := existingID.(string)
		v, ok := s.messages.Load(msgID)
		if ok {
			return v.(*message).clone(), false, nil
		}

		// Index exists but message doesn't - clean up stale index and retry
		s.idempotencyIdx.Delete(idxKey)
	}

	// We won the race - create the message with the ID we reserved
	now := time.Now().UTC()
	m := &message{
		id:        newMsgID,
		ownerID:   data.OwnerID,
		senderID:  data.SenderID,
		subject:   data.Subject,
		body:      data.Body,
		status:    data.Status,
		folderID:  data.FolderID,
		threadID:  data.ThreadID,
		replyToID: data.ReplyToID,
		createdAt: now,
		updatedAt: now,
		isDraft:   false,
	}

	if data.RecipientIDs != nil {
		m.recipientIDs = make([]string, len(data.RecipientIDs))
		copy(m.recipientIDs, data.RecipientIDs)
	}
	if data.Tags != nil {
		m.tags = make([]string, len(data.Tags))
		copy(m.tags, data.Tags)
	}
	if data.Metadata != nil {
		m.metadata = make(map[string]any, len(data.Metadata))
		for k, v := range data.Metadata {
			m.metadata[k] = v
		}
	}
	if data.Attachments != nil {
		m.attachments = make([]store.Attachment, len(data.Attachments))
		copy(m.attachments, data.Attachments)
	}

	s.messages.Store(m.id, m)
	return m.clone(), true, nil
}

// =============================================================================
// Maintenance Operations
// =============================================================================

// DeleteExpiredTrash atomically deletes all messages in trash older than cutoff.
//
// Safe to call concurrently - each message is deleted exactly once.
// Uses sync.Map.Range + Delete which is safe for concurrent access.
func (s *Store) DeleteExpiredTrash(ctx context.Context, cutoff time.Time) (int64, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return 0, store.ErrNotConnected
	}

	var deleted int64
	var toDelete []string

	// First pass: collect IDs to delete
	s.messages.Range(func(key, value any) bool {
		m := value.(*message)
		if !m.isDraft && m.folderID == store.FolderTrash {
			if m.updatedAt.Before(cutoff) {
				toDelete = append(toDelete, key.(string))
			}
		}
		return true
	})

	// Second pass: delete collected messages
	// Each Delete is atomic - concurrent calls will simply find nothing to delete
	for _, id := range toDelete {
		if _, loaded := s.messages.LoadAndDelete(id); loaded {
			deleted++
		}
	}

	return deleted, nil
}

// CountByFolders returns message counts and unread counts for the given folders.
// Implements store.FolderCounter for optimized batch counting.
func (s *Store) CountByFolders(ctx context.Context, ownerID string, folderIDs []string) (map[string]store.FolderCounts, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	folderSet := make(map[string]bool, len(folderIDs))
	for _, id := range folderIDs {
		folderSet[id] = true
	}

	counts := make(map[string]store.FolderCounts, len(folderIDs))
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.isDraft || m.ownerID != ownerID {
			return true
		}
		if !folderSet[m.folderID] {
			return true
		}
		c := counts[m.folderID]
		c.Total++
		if !m.isRead {
			c.Unread++
		}
		counts[m.folderID] = c
		return true
	})

	return counts, nil
}

// FindWithCount retrieves messages and total count in a single pass.
// Implements store.FindWithCounter for optimized list operations.
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
// Implements store.FolderLister for custom folder discovery.
func (s *Store) ListDistinctFolders(ctx context.Context, ownerID string) ([]string, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	seen := make(map[string]bool)
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.isDraft || m.ownerID != ownerID {
			return true
		}
		seen[m.folderID] = true
		return true
	})

	folders := make([]string, 0, len(seen))
	for id := range seen {
		folders = append(folders, id)
	}
	return folders, nil
}

// =============================================================================
// Stats Operations
// =============================================================================

// MailboxStats returns aggregate statistics for a user's mailbox in a single pass.
func (s *Store) MailboxStats(ctx context.Context, ownerID string) (*store.MailboxStats, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	stats := &store.MailboxStats{
		Folders: make(map[string]store.FolderCounts),
	}

	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.ownerID != ownerID {
			return true
		}
		if m.isDraft {
			stats.DraftCount++
			return true
		}
		stats.TotalMessages++
		if !m.isRead {
			stats.UnreadCount++
		}
		c := stats.Folders[m.folderID]
		c.Total++
		if !m.isRead {
			c.Unread++
		}
		stats.Folders[m.folderID] = c
		return true
	})

	return stats, nil
}
