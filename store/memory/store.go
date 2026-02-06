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

// Compile-time check that Store implements store.Store.
var _ store.Store = (*Store)(nil)
