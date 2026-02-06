package mailbox

import (
	"context"
	"io"

	"github.com/rbaliyan/mailbox/store"
)

// Type aliases for commonly used store types.
// These allow users to work with the mailbox package without importing store directly.
type (
	ListOptions = store.ListOptions
	SearchQuery = store.SearchQuery
	SortOrder   = store.SortOrder
)

// Re-exported sort order constants.
const (
	SortAsc  = store.SortAsc
	SortDesc = store.SortDesc
)

// Service manages the mailbox system (server-side).
// It handles connections to storage and creates mailbox clients.
type Service interface {
	// IsConnected returns true if the service is connected and ready.
	IsConnected() bool

	// Connect establishes connections to storage backends.
	Connect(ctx context.Context) error
	// Close closes all connections.
	Close(ctx context.Context) error
	// Client returns a mailbox client for the given user.
	// The returned client shares the service's connections.
	// Connection state is checked lazily on each operation; if the service
	// is not connected, operations will return ErrNotConnected.
	Client(userID string) Mailbox
	// CleanupTrash permanently deletes messages that have been in trash
	// longer than the configured retention period. Call this periodically
	// using your application's scheduler.
	CleanupTrash(ctx context.Context) (*CleanupTrashResult, error)
	// Events returns per-service event instances for subscribing and publishing.
	// Each service has its own events bound to its own event bus, enabling
	// independent event routing and parallel testing.
	Events() *ServiceEvents
}

// MessageReader provides single message retrieval.
type MessageReader interface {
	Get(ctx context.Context, messageID string) (Message, error)
}

// MessageLister provides message listing by folder.
type MessageLister interface {
	Folder(ctx context.Context, folderID string, opts store.ListOptions) (MessageList, error)
}

// MessageSearcher provides message search capability.
type MessageSearcher interface {
	Search(ctx context.Context, query SearchQuery) (MessageList, error)
}

// ThreadReader provides access to message threads.
type ThreadReader interface {
	// GetThread returns all messages in a thread, ordered by creation time.
	GetThread(ctx context.Context, threadID string, opts store.ListOptions) (MessageList, error)
	// GetReplies returns all direct replies to a message.
	GetReplies(ctx context.Context, messageID string, opts store.ListOptions) (MessageList, error)
}

// DraftLister provides draft listing.
type DraftLister interface {
	Drafts(ctx context.Context, opts store.ListOptions) (DraftList, error)
}

// FolderReader provides folder information.
type FolderReader interface {
	ListFolders(ctx context.Context) ([]FolderInfo, error)
}

// FolderInfo provides information about a folder.
type FolderInfo struct {
	// ID is the folder identifier (e.g., "__inbox", "custom-folder").
	ID string
	// Name is the display name for the folder.
	Name string
	// IsSystem indicates if this is a system folder (starts with "__").
	IsSystem bool
	// MessageCount is the total number of messages in the folder.
	MessageCount int64
	// UnreadCount is the number of unread messages in the folder.
	UnreadCount int64
}

// DraftListReader provides read-only access to a paginated list of drafts.
type DraftListReader interface {
	// All returns all drafts in this list.
	All() []Draft
	// Total returns the total count of drafts matching the query (not just this page).
	Total() int64
	// HasMore returns true if there are more drafts after this page.
	HasMore() bool
	// NextCursor returns the cursor for fetching the next page.
	NextCursor() string
}

// DraftListMutator provides bulk mutation operations on a list of drafts.
type DraftListMutator interface {
	// Delete deletes all drafts in this list.
	Delete(ctx context.Context) (*BulkResult, error)
	// Send sends all drafts in this list.
	// Returns results for each draft (success or failure).
	// Use result.SentMessages() to access the sent messages.
	Send(ctx context.Context) (*DraftSendResult, error)
}

// DraftList provides access to a paginated list of drafts with bulk operations.
//
// Composed of:
//   - DraftListReader: Read-only access (All, Total, HasMore, NextCursor)
//   - DraftListMutator: Bulk mutations (Delete, Send)
type DraftList interface {
	DraftListReader
	DraftListMutator
}

// MessageComposer provides message composition.
type MessageComposer interface {
	Compose() (Draft, error)
}

// AttachmentLoader provides attachment access.
type AttachmentLoader interface {
	LoadAttachment(ctx context.Context, messageID, attachmentID string) (io.ReadCloser, error)
}

// MessageStreamer provides streaming access to messages.
// Use streaming for memory-efficient processing of large result sets.
// For paginated UI with bulk operations, use MessageLister instead.
//
// Example - stream inbox messages:
//
//	iter, _ := mb.Stream(ctx, []store.Filter{store.InFolder(store.FolderInbox)}, mailbox.StreamOptions{BatchSize: 100})
//	for iter.Next(ctx) { msg, _ := iter.Message(); ... }
type MessageStreamer interface {
	// Stream returns an iterator for messages matching the given filters.
	// The owner filter and not-deleted filter are automatically added.
	Stream(ctx context.Context, filters []store.Filter, opts StreamOptions) (MessageIterator, error)
	// StreamSearch returns an iterator for search results.
	StreamSearch(ctx context.Context, query SearchQuery, opts StreamOptions) (MessageIterator, error)
}

// MessageClient provides all message-related read operations.
// Use this interface when you only need message access without draft/folder operations.
type MessageClient interface {
	MessageReader
	MessageLister
	MessageSearcher
	MessageStreamer
	ThreadReader
}

// DraftClient provides draft-related operations.
// Use this interface when you only need draft access.
type DraftClient interface {
	DraftLister
	MessageComposer
}

// StorageClient provides folder and attachment operations.
// Use this interface when you only need storage metadata access.
type StorageClient interface {
	FolderReader
	AttachmentLoader
}

// AttachmentResolver provides attachment metadata resolution by ID.
// This is useful for server integrations that need to resolve attachment
// references without loading the full message.
type AttachmentResolver interface {
	ResolveAttachments(ctx context.Context, attachmentIDs []string) ([]store.Attachment, error)
}

// MailboxMutator provides mutation operations on messages by ID.
// These methods are equivalent to calling Get() then mutating the Message,
// but skip the intermediate Get for efficiency in server integrations.
type MailboxMutator interface {
	UpdateFlags(ctx context.Context, messageID string, flags Flags) error
	MoveToFolder(ctx context.Context, messageID string, folderID string) error
	Delete(ctx context.Context, messageID string) error
	Restore(ctx context.Context, messageID string) error
	PermanentlyDelete(ctx context.Context, messageID string) error
	AddTag(ctx context.Context, messageID string, tagID string) error
	RemoveTag(ctx context.Context, messageID string, tagID string) error
	// MarkAllRead marks all unread messages in a folder as read.
	// Uses store.BulkReadMarker for a single database operation when available,
	// falling back to individual MarkRead calls otherwise.
	// Returns the number of messages that were marked as read.
	MarkAllRead(ctx context.Context, folderID string) (int64, error)
}

// SendRequest contains the data needed to send a message directly,
// without going through the draft composition flow.
type SendRequest struct {
	RecipientIDs  []string
	Subject       string
	Body          string
	Metadata      map[string]any
	Attachments   []store.Attachment
	AttachmentIDs []string
	ThreadID      string
	ReplyToID     string
}

// MessageSender provides direct message sending without drafts.
// This is useful for server integrations where the draft composition
// flow is handled externally (e.g., via gRPC).
type MessageSender interface {
	SendMessage(ctx context.Context, req SendRequest) (Message, error)
}

// BulkOperator provides bulk mutation operations by message IDs.
// Each method operates on a list of message IDs and returns a BulkResult
// with per-ID success/failure information.
type BulkOperator interface {
	BulkUpdateFlags(ctx context.Context, messageIDs []string, flags Flags) (*BulkResult, error)
	BulkMove(ctx context.Context, messageIDs []string, folderID string) (*BulkResult, error)
	BulkDelete(ctx context.Context, messageIDs []string) (*BulkResult, error)
	BulkPermanentlyDelete(ctx context.Context, messageIDs []string) (*BulkResult, error)
	BulkAddTag(ctx context.Context, messageIDs []string, tagID string) (*BulkResult, error)
	BulkRemoveTag(ctx context.Context, messageIDs []string, tagID string) (*BulkResult, error)
}

// Mailbox provides email-like messaging functionality for a user.
// This is the main interface for mailbox operations.
//
// Composed of focused client interfaces:
//   - MessageClient: All message read operations (Get, List, Search, Stream, Threads)
//   - DraftClient: Draft operations (List drafts, Compose new)
//   - StorageClient: Storage operations (Folders, Attachments)
//   - MailboxMutator: Direct mutation by message ID (UpdateFlags, Move, Delete, Tags)
//   - MessageSender: Direct message sending without drafts (SendMessage)
//   - BulkOperator: Bulk mutations by message IDs (BulkUpdateFlags, BulkMove, etc.)
//   - AttachmentResolver: Resolve attachment metadata by ID
//
// For applications needing only a subset of functionality, use the focused
// interfaces directly (MessageClient, DraftClient, StorageClient).
//
// For single message operations via a message handle, use the methods
// on the Message interface returned by Get().
//
// For bulk operations on listed messages, use the methods on MessageList:
//
//	inbox, _ := mailbox.Folder(ctx, store.FolderInbox, opts)
//	inbox.MarkRead(ctx)           // mark all as read
//	inbox.Move(ctx, "archive")    // move all to archive
//	inbox.Delete(ctx)             // delete all
type Mailbox interface {
	UserID() string
	MessageClient
	DraftClient
	StorageClient
	MailboxMutator
	MessageSender
	BulkOperator
	AttachmentResolver
	// Stats returns aggregate statistics for this user's mailbox.
	Stats(ctx context.Context) (*store.MailboxStats, error)
	// UnreadCount returns the total unread message count for this user.
	// This is a convenience method equivalent to calling Stats() and reading UnreadCount.
	UnreadCount(ctx context.Context) (int64, error)
}
