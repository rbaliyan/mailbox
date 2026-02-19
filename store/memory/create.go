package memory

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rbaliyan/mailbox/store"
)

// newMessageFromData creates a message struct from MessageData.
// Shared by CreateMessage and CreateMessageIdempotent to avoid duplication.
func newMessageFromData(data store.MessageData, id string, now time.Time) *message {
	m := &message{
		id:        id,
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
	}

	if data.RecipientIDs != nil {
		m.recipientIDs = make([]string, len(data.RecipientIDs))
		copy(m.recipientIDs, data.RecipientIDs)
	}
	if data.Tags != nil {
		m.tags = make([]string, len(data.Tags))
		copy(m.tags, data.Tags)
	}
	if data.Headers != nil {
		m.headers = make(map[string]string, len(data.Headers))
		for k, v := range data.Headers {
			m.headers[k] = v
		}
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

	return m
}

// CreateMessage creates a new message from the given data.
func (s *Store) CreateMessage(ctx context.Context, data store.MessageData) (store.Message, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	m := newMessageFromData(data, uuid.New().String(), now)

	s.messages.Store(m.id, m)
	return m.clone(), nil
}

// CreateMessages creates multiple messages atomically.
// For the memory store, this uses a simple loop since sync.Map operations
// are already atomic per-key. In production stores, this should use
// database transactions for true atomicity.
func (s *Store) CreateMessages(ctx context.Context, data []store.MessageData) ([]store.Message, error) {
	if err := s.checkConnected(); err != nil {
		return nil, err
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
	if err := s.checkConnected(); err != nil {
		return nil, false, err
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
	m := newMessageFromData(data, newMsgID, now)

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
	if err := s.checkConnected(); err != nil {
		return 0, err
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
