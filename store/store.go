// Package store provides interfaces and types for mailbox storage.
// Implementations are in store/mongo, store/memory, and store/postgres subpackages.
//
// # Architectural Principle: No Distributed Locks
//
// This package is designed to avoid distributed locks entirely. Distributed locks
// introduce complexity, single points of failure, and performance bottlenecks.
// Instead, all concurrency concerns are handled through:
//
//  1. Atomic Database Operations: Use database-native atomic operations like
//     MongoDB's findOneAndUpdate with upsert, or PostgreSQL's INSERT ON CONFLICT.
//     These operations are guaranteed to be atomic by the database engine.
//
//  2. Idempotency via Unique Constraints: Instead of locking before write,
//     use unique indexes/constraints and handle conflicts via return status.
//     The database enforces uniqueness atomically - no external coordination needed.
//
//  3. Optimistic Concurrency: For updates, use version fields or timestamps
//     and let the database reject stale updates. Retry on conflict.
//
//  4. Transactional Batches: Multi-document operations use database transactions
//     (MongoDB sessions, PostgreSQL transactions) for atomicity, not distributed locks.
//
// Example - Idempotent Message Send:
//
//	// WRONG: Distributed lock approach (DO NOT USE)
//	lock.Acquire("send:" + idempotencyKey)
//	defer lock.Release()
//	if exists := store.Get(idempotencyKey); exists { return exists }
//	msg := store.Create(data)
//	return msg
//
//	// CORRECT: Atomic upsert approach
//	msg, created, err := store.CreateMessageIdempotent(ctx, data, idempotencyKey)
//	if !created {
//	    return msg, nil  // Already existed, return cached result
//	}
//	return msg, nil  // Newly created
//
// Example - Concurrent Trash Cleanup:
//
//	// WRONG: Distributed lock approach (DO NOT USE)
//	if !lock.TryAcquire("trash-cleanup") { return }
//	messages := store.FindExpired()
//	for _, msg := range messages { store.Delete(msg) }
//
//	// CORRECT: Atomic bulk delete
//	deleted, err := store.DeleteExpiredTrash(ctx, cutoff)
//	// Multiple instances can call this safely - database handles atomicity
//
// This design provides:
//   - Simpler architecture (no external lock service like Redis/Consul/etcd)
//   - Better reliability (database ACID guarantees vs lock service availability)
//   - Higher performance (no extra round-trips for lock acquire/release)
//   - Automatic deadlock prevention (no distributed deadlocks possible)
//   - Cleaner failure handling (database transactions auto-rollback)
package store

import (
	"context"
	"time"
)

// Store is the storage interface for the mailbox.
// It provides separate operations for drafts (mutable) and messages (read-only).
//
// All operations must be safe for concurrent use. Implementations must use
// database-level atomicity (transactions, atomic operations) rather than
// external locking mechanisms. See package documentation for details.
type Store interface {
	// Lifecycle
	Connect(ctx context.Context) error
	Close(ctx context.Context) error

	// Draft operations - drafts are mutable messages being composed
	DraftStore

	// Message operations - messages are read-only sent/received items
	MessageStore

	// Maintenance operations - for background cleanup tasks
	MaintenanceStore

	// Stats operations - aggregate mailbox statistics
	StatsStore
}

// DraftStore provides operations for draft messages.
// Drafts are mutable and owned by a single user.
type DraftStore interface {
	// NewDraft creates a new empty draft for the given owner.
	// This is the only way to create a DraftMessage.
	NewDraft(ownerID string) DraftMessage

	// GetDraft retrieves a draft by ID.
	// Returns ErrNotFound if the draft doesn't exist.
	GetDraft(ctx context.Context, id string) (DraftMessage, error)

	// SaveDraft persists a draft. If the draft has no ID, a new one is assigned.
	// Returns the saved draft (may have updated fields like ID, timestamps).
	SaveDraft(ctx context.Context, draft DraftMessage) (DraftMessage, error)

	// DeleteDraft permanently removes a draft.
	// Returns ErrNotFound if the draft doesn't exist.
	DeleteDraft(ctx context.Context, id string) error

	// ListDrafts returns all drafts for a user.
	ListDrafts(ctx context.Context, ownerID string, opts ListOptions) (*DraftList, error)
}

// MessageStoreReader provides read operations for messages.
type MessageStoreReader interface {
	// Get retrieves a message by ID.
	// Returns ErrNotFound if the message doesn't exist.
	Get(ctx context.Context, id string) (Message, error)

	// Find retrieves messages matching the filters.
	Find(ctx context.Context, filters []Filter, opts ListOptions) (*MessageList, error)

	// Count returns the count of messages matching the filters.
	Count(ctx context.Context, filters []Filter) (int64, error)

	// Search performs full-text search on messages.
	Search(ctx context.Context, query SearchQuery) (*MessageList, error)
}

// MessageStoreMutator provides mutation operations for messages.
// Mutations are specific operations, not general setters.
type MessageStoreMutator interface {
	// MarkRead sets the read status of a message.
	MarkRead(ctx context.Context, id string, read bool) error

	// MoveToFolder moves a message to a different folder.
	MoveToFolder(ctx context.Context, id string, folderID string) error

	// AddTag adds a tag to a message.
	AddTag(ctx context.Context, id string, tagID string) error

	// RemoveTag removes a tag from a message.
	RemoveTag(ctx context.Context, id string, tagID string) error

	// Delete soft-deletes a message (moves to trash).
	Delete(ctx context.Context, id string) error

	// HardDelete permanently removes a message.
	HardDelete(ctx context.Context, id string) error

	// Restore restores a soft-deleted message from trash.
	Restore(ctx context.Context, id string) error
}

// MessageStoreCreator provides message creation operations.
//
// Concurrency: All operations are safe for concurrent use and rely on
// database-level atomicity. No external locking is required or desired.
type MessageStoreCreator interface {
	// CreateMessage creates a new message from the given data.
	// Used internally when sending a draft to create sender/recipient copies.
	CreateMessage(ctx context.Context, data MessageData) (Message, error)

	// CreateMessageIdempotent atomically creates a message or returns existing.
	//
	// This operation MUST be atomic at the database level using mechanisms like:
	//   - MongoDB: findOneAndUpdate with upsert
	//   - PostgreSQL: INSERT ... ON CONFLICT DO NOTHING RETURNING ...
	//
	// The idempotency key combined with owner ID forms a unique constraint.
	// If a message with the same (ownerID, idempotencyKey) exists, it is returned
	// without modification and created=false.
	//
	// This design eliminates the need for distributed locks when handling
	// duplicate requests (e.g., network retries, user double-clicks).
	//
	// Returns:
	//   - (message, true, nil): New message was created
	//   - (message, false, nil): Existing message was found and returned
	//   - (nil, false, error): Operation failed
	CreateMessageIdempotent(ctx context.Context, data MessageData, idempotencyKey string) (Message, bool, error)

	// CreateMessages creates multiple messages atomically in a single transaction.
	//
	// This operation MUST be atomic - either all messages are created or none are.
	// Implementations should use:
	//   - MongoDB: insertMany with ordered=true in a session/transaction
	//   - PostgreSQL: Single INSERT with multiple VALUES in a transaction
	//
	// This atomicity guarantee eliminates the need for distributed locks when
	// sending to multiple recipients. If the operation fails, callers know
	// that no partial state exists - they can safely retry the entire batch.
	//
	// Returns:
	//   - (messages, nil): All messages created successfully
	//   - (nil, error): Operation failed, no messages were created
	CreateMessages(ctx context.Context, data []MessageData) ([]Message, error)
}

// MessageStore provides operations for sent/received messages.
// Messages are read-only - modifications are done via specific operations.
//
// Composed of:
//   - MessageStoreReader: Read operations (Get, Find, Count, Search)
//   - MessageStoreMutator: Mutation operations (MarkRead, MoveToFolder, tags, delete)
//   - MessageStoreCreator: Creation operations (CreateMessage, CreateMessageIdempotent)
//
// Concurrency: All operations are safe for concurrent use and rely on
// database-level atomicity. No external locking is required or desired.
type MessageStore interface {
	MessageStoreReader
	MessageStoreMutator
	MessageStoreCreator
}

// FolderCounts holds the message and unread counts for a folder.
type FolderCounts struct {
	Total  int64
	Unread int64
}

// FolderCounter is an optional interface that Store implementations can
// implement to provide optimized batch folder counting.
// When implemented, ListFolders uses a single query instead of N separate
// Count calls (one per folder).
type FolderCounter interface {
	// CountByFolders returns message counts and unread counts for the given folders.
	// The returned map is keyed by folder ID. Missing keys indicate zero counts.
	CountByFolders(ctx context.Context, ownerID string, folderIDs []string) (map[string]FolderCounts, error)
}

// FindWithCounter is an optional interface that Store implementations can
// implement to return messages and total count in a single query.
// When implemented, list operations avoid a separate Count round-trip.
type FindWithCounter interface {
	// FindWithCount retrieves messages matching the filters and returns
	// both the messages and the total count in a single operation.
	FindWithCount(ctx context.Context, filters []Filter, opts ListOptions) (*MessageList, int64, error)
}

// FolderLister is an optional interface that Store implementations can
// implement to discover custom (non-system) folders for a user.
// When implemented, ListFolders includes custom folders alongside system folders.
type FolderLister interface {
	// ListDistinctFolders returns all distinct folder IDs for a user's non-deleted messages.
	// This is used to discover custom folders that are not system folders.
	ListDistinctFolders(ctx context.Context, ownerID string) ([]string, error)
}

// BulkReadMarker is an optional interface for efficient bulk read marking.
// When implemented, MarkAllRead uses a single database operation instead of
// N individual MarkRead calls. All three built-in backends implement this.
type BulkReadMarker interface {
	// MarkAllRead marks all unread non-draft messages in a folder as read.
	// Returns the number of messages that were marked as read.
	MarkAllRead(ctx context.Context, ownerID string, folderID string) (int64, error)
}

// MaintenanceStore provides operations for background maintenance tasks.
// These operations are designed to be safely called concurrently from
// multiple service instances without requiring distributed coordination.
type MaintenanceStore interface {
	// DeleteExpiredTrash atomically deletes all messages in trash older than cutoff.
	//
	// This operation is safe to call concurrently from multiple instances.
	// The database handles atomicity - if two instances call this simultaneously,
	// each message is deleted exactly once (one instance succeeds, the other
	// finds no matching documents).
	//
	// Implementation should use atomic bulk delete:
	//   - MongoDB: deleteMany({ folder: "__trash", trashedAt: { $lt: cutoff } })
	//   - PostgreSQL: DELETE FROM messages WHERE folder = '__trash' AND trashed_at < $1
	//
	// Returns the number of messages deleted and any error encountered.
	DeleteExpiredTrash(ctx context.Context, cutoff time.Time) (int64, error)
}
