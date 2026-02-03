// Package memory provides an in-memory Store implementation for testing.
// This store is not suitable for production use - data is not persisted.
package memory

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rbaliyan/mailbox/store"
)

// Store implements store.Store with in-memory storage.
// Thread-safe for concurrent use. Not suitable for production.
type Store struct {
	messages       sync.Map // map[string]*message (both drafts and messages)
	idempotencyIdx sync.Map // map[string]string (ownerID:idempotencyKey -> messageID)
	msgLocks       sync.Map // map[string]*sync.Mutex (per-message locks for mutations)
	connected      int32
}

// getMsgLock returns the mutex for a message ID, creating one if needed.
// Uses LoadOrStore for atomic get-or-create.
func (s *Store) getMsgLock(id string) *sync.Mutex {
	lock, _ := s.msgLocks.LoadOrStore(id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// New creates a new in-memory store.
func New() *Store {
	return &Store{}
}

// Connect marks the store as connected.
func (s *Store) Connect(_ context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.connected, 0, 1) {
		return store.ErrAlreadyConnected
	}
	return nil
}

// Close marks the store as disconnected.
func (s *Store) Close(_ context.Context) error {
	atomic.StoreInt32(&s.connected, 0)
	return nil
}

// =============================================================================
// Draft Operations
// =============================================================================

// NewDraft creates a new empty draft for the given owner.
func (s *Store) NewDraft(ownerID string) store.DraftMessage {
	now := time.Now().UTC()
	return &message{
		ownerID:   ownerID,
		senderID:  ownerID, // sender is always the owner for drafts
		metadata:  make(map[string]any),
		status:    store.MessageStatusDraft,
		createdAt: now,
		updatedAt: now,
		isDraft:   true,
	}
}

// GetDraft retrieves a draft by ID.
func (s *Store) GetDraft(ctx context.Context, id string) (store.DraftMessage, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}
	if id == "" {
		return nil, store.ErrInvalidID
	}

	v, ok := s.messages.Load(id)
	if !ok {
		return nil, store.ErrNotFound
	}

	m := v.(*message)
	if !m.isDraft {
		return nil, store.ErrNotFound // not a draft
	}

	return m.clone(), nil
}

// SaveDraft persists a draft.
func (s *Store) SaveDraft(ctx context.Context, draft store.DraftMessage) (store.DraftMessage, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	// Fast path: draft created by this store.
	m, ok := draft.(*message)
	if !ok {
		// Slow path: build from interface (supports any DraftMessage implementation).
		m = &message{
			id:           draft.GetID(),
			ownerID:      draft.GetOwnerID(),
			senderID:     draft.GetSenderID(),
			subject:      draft.GetSubject(),
			body:         draft.GetBody(),
			recipientIDs: draft.GetRecipientIDs(),
			createdAt:    draft.GetCreatedAt(),
			updatedAt:    draft.GetUpdatedAt(),
			isDraft:      true,
		}
		if meta := draft.GetMetadata(); meta != nil {
			m.metadata = make(map[string]any, len(meta))
			for k, v := range meta {
				m.metadata[k] = v
			}
		} else {
			m.metadata = make(map[string]any)
		}
		if attachments := draft.GetAttachments(); attachments != nil {
			m.attachments = make([]store.Attachment, len(attachments))
			copy(m.attachments, attachments)
		}
	}

	// Assign ID if new
	if m.id == "" {
		m.id = uuid.New().String()
	}

	m.updatedAt = time.Now().UTC()
	m.isDraft = true

	// Store a copy
	s.messages.Store(m.id, m.clone())

	return m.clone(), nil
}

// DeleteDraft permanently removes a draft.
func (s *Store) DeleteDraft(ctx context.Context, id string) error {
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
	if !m.isDraft {
		return store.ErrNotFound // not a draft
	}

	s.messages.Delete(id)
	return nil
}

// ListDrafts returns all drafts for a user.
func (s *Store) ListDrafts(ctx context.Context, ownerID string, opts store.ListOptions) (*store.DraftList, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	var drafts []*message
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.isDraft && m.ownerID == ownerID {
			drafts = append(drafts, m)
		}
		return true
	})

	// Sort by created_at descending (newest first)
	sortMessages(drafts, "created_at", store.SortDesc)

	// Apply pagination
	total := int64(len(drafts))
	start := opts.Offset
	if start > len(drafts) {
		start = len(drafts)
	}
	end := start + opts.Limit
	if opts.Limit == 0 {
		end = len(drafts)
	}
	if end > len(drafts) {
		end = len(drafts)
	}

	result := drafts[start:end]
	draftList := make([]store.DraftMessage, len(result))
	for i, m := range result {
		draftList[i] = m.clone()
	}

	return &store.DraftList{
		Drafts:  draftList,
		Total:   total,
		HasMore: end < len(drafts),
	}, nil
}

// =============================================================================
// Message Operations
// =============================================================================

// Get retrieves a message by ID.
func (s *Store) Get(ctx context.Context, id string) (store.Message, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}
	if id == "" {
		return nil, store.ErrInvalidID
	}

	v, ok := s.messages.Load(id)
	if !ok {
		return nil, store.ErrNotFound
	}

	m := v.(*message)
	if m.isDraft {
		return nil, store.ErrNotFound // drafts are not messages
	}

	return m.clone(), nil
}

// Find retrieves messages matching the filters.
func (s *Store) Find(ctx context.Context, filters []store.Filter, opts store.ListOptions) (*store.MessageList, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	var all []*message
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if !m.isDraft && matchesFilters(m, filters) {
			all = append(all, m)
		}
		return true
	})

	// Sort
	sortBy := opts.SortBy
	if sortBy == "" {
		sortBy = "created_at"
	}
	sortMessages(all, sortBy, opts.SortOrder)

	total := int64(len(all))

	// Apply cursor-based pagination using StartAfter
	start := 0
	if opts.StartAfter != "" {
		found := false
		for i, m := range all {
			if m.id == opts.StartAfter {
				start = i + 1 // Start after this message
				found = true
				break
			}
		}
		if !found {
			// Cursor not found (deleted). Return empty results since the page
			// boundary is unknown. Callers should re-query without a cursor.
			return &store.MessageList{Total: total}, nil
		}
	}

	end := start + opts.Limit
	if opts.Limit == 0 {
		end = len(all)
	}
	if end > len(all) {
		end = len(all)
	}

	result := all[start:end]
	messages := make([]store.Message, len(result))
	for i, m := range result {
		messages[i] = m.clone()
	}

	return &store.MessageList{
		Messages: messages,
		Total:    total,
		HasMore:  end < len(all),
	}, nil
}

// Count returns the count of messages matching the filters.
func (s *Store) Count(ctx context.Context, filters []store.Filter) (int64, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return 0, store.ErrNotConnected
	}

	var count int64
	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if !m.isDraft && matchesFilters(m, filters) {
			count++
		}
		return true
	})
	return count, nil
}

// Search performs full-text search on messages.
func (s *Store) Search(ctx context.Context, query store.SearchQuery) (*store.MessageList, error) {
	if atomic.LoadInt32(&s.connected) == 0 {
		return nil, store.ErrNotConnected
	}

	var all []*message
	searchTerm := strings.ToLower(query.Query)

	s.messages.Range(func(_, v any) bool {
		m := v.(*message)
		if m.isDraft {
			return true
		}
		if query.OwnerID != "" && m.ownerID != query.OwnerID {
			return true
		}
		if !matchesFilters(m, query.Filters) {
			return true
		}
		if len(query.Tags) > 0 && !hasAllTags(m, query.Tags) {
			return true
		}
		if searchTerm != "" {
			if !strings.Contains(strings.ToLower(m.subject), searchTerm) &&
				!strings.Contains(strings.ToLower(m.body), searchTerm) {
				return true
			}
		}
		all = append(all, m)
		return true
	})

	// Sort and paginate
	sortMessages(all, query.Options.SortBy, query.Options.SortOrder)

	total := int64(len(all))

	// Apply cursor-based pagination using StartAfter
	start := 0
	if query.Options.StartAfter != "" {
		found := false
		for i, m := range all {
			if m.id == query.Options.StartAfter {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return &store.MessageList{Total: total}, nil
		}
	}

	end := start + query.Options.Limit
	if query.Options.Limit == 0 {
		end = len(all)
	}
	if end > len(all) {
		end = len(all)
	}

	result := all[start:end]
	messages := make([]store.Message, len(result))
	for i, m := range result {
		messages[i] = m.clone()
	}

	return &store.MessageList{
		Messages: messages,
		Total:    total,
		HasMore:  end < len(all),
	}, nil
}

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
// Helper Functions
// =============================================================================

func matchesFilters(m *message, filters []store.Filter) bool {
	for _, f := range filters {
		if !matchesFilter(m, f) {
			return false
		}
	}
	return true
}

func matchesFilter(m *message, f store.Filter) bool {
	key := f.Key()
	value := f.Value()
	op := f.Operator()

	// Handle slice-based fields with special operators.
	switch key {
	case "tags":
		return matchesSliceFilter(m.tags, op, value)
	case "recipient_ids":
		return matchesSliceFilter(m.recipientIDs, op, value)
	}

	// Scalar fields.
	var fieldValue any
	switch key {
	case "id":
		fieldValue = m.id
	case "owner_id":
		fieldValue = m.ownerID
	case "sender_id":
		fieldValue = m.senderID
	case "subject":
		fieldValue = m.subject
	case "folder_id":
		fieldValue = m.folderID
	case "is_read":
		fieldValue = m.isRead
	case "status":
		fieldValue = m.status
	case "thread_id":
		fieldValue = m.threadID
	case "reply_to_id":
		fieldValue = m.replyToID
	case "created_at":
		fieldValue = m.createdAt
	case "updated_at":
		fieldValue = m.updatedAt
	default:
		return true // Unknown field, skip filter
	}

	switch op {
	case "eq", "=", "":
		return fieldValue == value
	case "ne", "!=":
		return fieldValue != value
	case "lt", "<":
		return compareValues(fieldValue, value) < 0
	case "lte", "<=":
		return compareValues(fieldValue, value) <= 0
	case "gt", ">":
		return compareValues(fieldValue, value) > 0
	case "gte", ">=":
		return compareValues(fieldValue, value) >= 0
	case "exists":
		exists, _ := value.(bool)
		isEmpty := fieldValue == "" || fieldValue == nil
		return exists != isEmpty
	case "in":
		return valueInSet(fieldValue, value)
	case "nin":
		return !valueInSet(fieldValue, value)
	default:
		return true
	}
}

// matchesSliceFilter handles filter operations on slice fields (tags, recipient_ids).
func matchesSliceFilter(slice []string, op string, value any) bool {
	switch op {
	case "contains":
		s, ok := value.(string)
		if !ok {
			return false
		}
		for _, item := range slice {
			if item == s {
				return true
			}
		}
		return false
	case "exists":
		exists, _ := value.(bool)
		hasItems := len(slice) > 0
		return exists == hasItems
	case "eq", "=", "":
		// Equality on slice: check if all elements match
		other, ok := value.([]string)
		if !ok {
			return false
		}
		if len(slice) != len(other) {
			return false
		}
		for i := range slice {
			if slice[i] != other[i] {
				return false
			}
		}
		return true
	default:
		return true
	}
}

// valueInSet checks if a scalar value is in a set (slice) of values.
func valueInSet(fieldValue any, set any) bool {
	switch s := set.(type) {
	case []string:
		fv, ok := fieldValue.(string)
		if !ok {
			return false
		}
		for _, v := range s {
			if v == fv {
				return true
			}
		}
	case []any:
		for _, v := range s {
			if v == fieldValue {
				return true
			}
		}
	}
	return false
}

func compareValues(a, b any) int {
	switch av := a.(type) {
	case string:
		if bv, ok := b.(string); ok {
			return strings.Compare(av, bv)
		}
	case int:
		if bv, ok := b.(int); ok {
			if av < bv {
				return -1
			} else if av > bv {
				return 1
			}
			return 0
		}
	case int64:
		if bv, ok := b.(int64); ok {
			if av < bv {
				return -1
			} else if av > bv {
				return 1
			}
			return 0
		}
	case time.Time:
		if bv, ok := b.(time.Time); ok {
			if av.Before(bv) {
				return -1
			} else if av.After(bv) {
				return 1
			}
			return 0
		}
	}
	return 0
}

func hasAllTags(m *message, tags []string) bool {
	tagSet := make(map[string]bool, len(m.tags))
	for _, t := range m.tags {
		tagSet[t] = true
	}
	for _, t := range tags {
		if !tagSet[t] {
			return false
		}
	}
	return true
}

func sortMessages(msgs []*message, sortBy string, order store.SortOrder) {
	if sortBy == "" {
		sortBy = "created_at"
	}
	if order == 0 {
		order = store.SortDesc
	}

	// Simple bubble sort for testing
	for i := 0; i < len(msgs)-1; i++ {
		for j := i + 1; j < len(msgs); j++ {
			shouldSwap := false
			switch sortBy {
			case "created_at":
				if order == store.SortAsc {
					shouldSwap = msgs[i].createdAt.After(msgs[j].createdAt)
				} else {
					shouldSwap = msgs[i].createdAt.Before(msgs[j].createdAt)
				}
			case "updated_at":
				if order == store.SortAsc {
					shouldSwap = msgs[i].updatedAt.After(msgs[j].updatedAt)
				} else {
					shouldSwap = msgs[i].updatedAt.Before(msgs[j].updatedAt)
				}
			case "subject":
				if order == store.SortAsc {
					shouldSwap = msgs[i].subject > msgs[j].subject
				} else {
					shouldSwap = msgs[i].subject < msgs[j].subject
				}
			}
			if shouldSwap {
				msgs[i], msgs[j] = msgs[j], msgs[i]
			}
		}
	}
}

// Compile-time check that Store implements store.Store.
var _ store.Store = (*Store)(nil)
