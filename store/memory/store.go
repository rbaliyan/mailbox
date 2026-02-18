// Package memory provides an in-memory Store implementation for testing.
// This store is not suitable for production use - data is not persisted.
package memory

import (
	"context"
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

// checkConnected returns ErrNotConnected if the store is not connected.
func (s *Store) checkConnected() error {
	if atomic.LoadInt32(&s.connected) == 0 {
		return store.ErrNotConnected
	}
	return nil
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
	if err := s.checkConnected(); err != nil {
		return nil, err
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
	if err := s.checkConnected(); err != nil {
		return nil, err
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
		if headers := draft.GetHeaders(); headers != nil {
			m.headers = make(map[string]string, len(headers))
			for k, v := range headers {
				m.headers[k] = v
			}
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
	if err := s.checkConnected(); err != nil {
		return err
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
	if err := s.checkConnected(); err != nil {
		return nil, err
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

// Compile-time check that Store implements store.Store.
var (
	_ store.Store           = (*Store)(nil)
	_ store.FolderCounter   = (*Store)(nil)
	_ store.FindWithCounter = (*Store)(nil)
	_ store.FolderLister    = (*Store)(nil)
)
