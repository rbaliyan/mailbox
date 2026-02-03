package mailbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event/v3/transport/noop"
	eventredis "github.com/rbaliyan/event/v3/transport/redis"
	"github.com/rbaliyan/mailbox/store"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/semaphore"
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

// ServiceHealth provides health and state information about the service.
type ServiceHealth interface {
	// IsConnected returns true if the service is connected and ready.
	IsConnected() bool
}

// Service manages the mailbox system (server-side).
// It handles connections to storage and creates mailbox clients.
//
// Composed of:
//   - ServiceHealth: Health and state queries (IsConnected)
type Service interface {
	ServiceHealth

	// Connect establishes connections to storage backends.
	Connect(ctx context.Context) error
	// Close closes all connections.
	Close(ctx context.Context) error
	// Client returns a mailbox client for the given user.
	// The returned client shares the service's connections.
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
	Inbox(ctx context.Context, opts store.ListOptions) (MessageList, error)
	Sent(ctx context.Context, opts store.ListOptions) (MessageList, error)
	Archived(ctx context.Context, opts store.ListOptions) (MessageList, error)
	Trash(ctx context.Context, opts store.ListOptions) (MessageList, error)
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
	// IDs returns the IDs of all drafts in this list.
	IDs() []string
}

// DraftListMutator provides bulk mutation operations on a list of drafts.
type DraftListMutator interface {
	// Delete deletes all drafts in this list.
	Delete(ctx context.Context) (*BulkResult, error)
	// Send sends all drafts in this list.
	// Returns results for each draft (success or failure).
	// Use result.SentMessages() to access the sent messages.
	Send(ctx context.Context) (*BulkResult, error)
}

// DraftList provides access to a paginated list of drafts with bulk operations.
//
// Composed of:
//   - DraftListReader: Read-only access (All, Total, HasMore, NextCursor, IDs)
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
//	inbox, _ := mailbox.Inbox(ctx, opts)
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
}

// Connection states for the service.
const (
	stateDisconnected int32 = 0
	stateConnecting   int32 = 1
	stateConnected    int32 = 2
)

// service is the default implementation of Service.
type service struct {
	store       store.Store
	attachments store.AttachmentManager
	logger      *slog.Logger
	opts        *options
	state       int32 // stateDisconnected, stateConnecting, or stateConnected
	plugins     *pluginRegistry
	otel        *otelInstrumentation
	sendSem     *semaphore.Weighted // Limits concurrent sends to prevent resource exhaustion
	eventBus    *event.Bus          // Event bus for publishing events
	events      *ServiceEvents      // Per-service event instances
}

// NewService creates a new mailbox service.
// Call Connect() to establish connections to backends.
//
// Caching is NOT included in this library. If you need caching, wrap your store
// with a caching decorator (see store/cached package for an example).
// This keeps the library focused on messaging while letting you control caching strategy.
func NewService(opts ...Option) (Service, error) {
	o := newOptions(opts...)

	if o.store == nil {
		return nil, ErrStoreRequired
	}

	// Initialize plugin registry
	plugins := newPluginRegistry(o.logger)
	for _, p := range o.plugins {
		plugins.register(p)
	}

	// Initialize OTel instrumentation
	otelInstr, err := newOtelInstrumentation(o)
	if err != nil {
		return nil, fmt.Errorf("init otel: %w", err)
	}

	return &service{
		store:       o.store,
		attachments: o.attachments,
		logger:      o.logger,
		opts:        o,
		plugins:     plugins,
		otel:        otelInstr,
		sendSem:     semaphore.NewWeighted(int64(o.maxConcurrentSends)),
	}, nil
}

// Events returns per-service event instances for subscribing and publishing.
func (s *service) Events() *ServiceEvents {
	return s.events
}

// IsConnected returns true if the service is connected and ready.
func (s *service) IsConnected() bool {
	return atomic.LoadInt32(&s.state) == stateConnected
}

// Connect establishes connections to storage backends.
func (s *service) Connect(ctx context.Context) error {
	// Use three-state to prevent Client() from seeing partial initialization
	// stateDisconnected -> stateConnecting -> stateConnected
	if !atomic.CompareAndSwapInt32(&s.state, stateDisconnected, stateConnecting) {
		if atomic.LoadInt32(&s.state) == stateConnecting {
			return ErrAlreadyConnected // Connection in progress
		}
		return ErrAlreadyConnected
	}

	// Reset to disconnected on failure, set to connected on success
	success := false
	defer func() {
		if success {
			atomic.StoreInt32(&s.state, stateConnected)
		} else {
			atomic.StoreInt32(&s.state, stateDisconnected)
		}
	}()

	if err := s.store.Connect(ctx); err != nil {
		return fmt.Errorf("connect store: %w", err)
	}

	// Initialize event bus with appropriate transport
	if err := s.initEventBus(ctx); err != nil {
		s.store.Close(ctx)
		return fmt.Errorf("init event bus: %w", err)
	}

	// Initialize plugins
	if err := s.plugins.initAll(ctx); err != nil {
		s.eventBus.Close(ctx)
		s.store.Close(ctx)
		return fmt.Errorf("init plugins: %w", err)
	}

	success = true
	s.logger.Info("mailbox service connected")
	return nil
}

// busCounter generates unique suffixes for event bus names.
var busCounter int64

// initEventBus initializes the event bus for this service.
// Each service creates its own bus. Events are global singletons that get
// bound to the first bus that registers them.
func (s *service) initEventBus(ctx context.Context) error {
	serviceName := s.opts.serviceName
	if serviceName == "" {
		serviceName = "mailbox"
	}
	// Each bus needs a unique name, so append a counter suffix
	busName := fmt.Sprintf("%s-%d", serviceName, atomic.AddInt64(&busCounter, 1))

	var bus *event.Bus
	var err error

	switch {
	case s.opts.eventTransport != nil:
		s.logger.Info("initializing event bus with custom transport")
		bus, err = event.NewBus(busName, event.WithTransport(s.opts.eventTransport))
	case s.opts.redisClient != nil:
		s.logger.Info("initializing event bus with Redis transport")
		t, transportErr := eventredis.New(s.opts.redisClient)
		if transportErr != nil {
			return fmt.Errorf("create redis transport: %w", transportErr)
		}
		bus, err = event.NewBus(busName, event.WithTransport(t))
	default:
		s.logger.Debug("initializing event bus with noop transport")
		bus, err = event.NewBus(busName, event.WithTransport(noop.New()))
	}

	if err != nil {
		return fmt.Errorf("create event bus: %w", err)
	}
	s.eventBus = bus

	// Create and register per-service events (unique per service instance).
	s.events = newServiceEvents(busName)
	if err := registerServiceEvents(ctx, bus, s.events); err != nil {
		bus.Close(ctx)
		return fmt.Errorf("register service events: %w", err)
	}

	// Also register global events for backward compatibility.
	// Global events use "first registration wins" - subsequent calls are no-ops.
	if err := registerEvents(ctx, bus); err != nil {
		bus.Close(ctx)
		return fmt.Errorf("register events: %w", err)
	}

	return nil
}

// Close closes connections to storage backends.
func (s *service) Close(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.state, stateConnected, stateDisconnected) {
		return nil
	}

	var errs []error

	// Wait for in-flight send operations to complete (graceful shutdown).
	// After setting state to disconnected, no new sends can start because checkAccess fails.
	// We acquire all semaphore slots to wait for existing operations to finish.
	s.logger.Info("waiting for in-flight operations to complete...", "timeout", s.opts.shutdownTimeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, s.opts.shutdownTimeout)
	defer shutdownCancel()
	if err := s.sendSem.Acquire(shutdownCtx, int64(s.opts.maxConcurrentSends)); err != nil {
		// Context cancelled or deadline exceeded - log but continue shutdown
		s.logger.Warn("timeout waiting for in-flight operations, proceeding with shutdown",
			"error", err)
		errs = append(errs, fmt.Errorf("graceful shutdown timeout: %w", err))
	} else {
		s.sendSem.Release(int64(s.opts.maxConcurrentSends))
		s.logger.Info("all in-flight operations completed")
	}

	// Close plugins first (reverse order of init)
	if err := s.plugins.closeAll(ctx); err != nil {
		errs = append(errs, fmt.Errorf("close plugins: %w", err))
	}

	// Close event bus only if using a real transport.
	// For noop transport, the bus doesn't hold resources and closing would
	// break events for other services sharing the same global events.
	if s.eventBus != nil && (s.opts.eventTransport != nil || s.opts.redisClient != nil) {
		if err := s.eventBus.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close event bus: %w", err))
		}
	}

	if err := s.store.Close(ctx); err != nil {
		errs = append(errs, fmt.Errorf("close store: %w", err))
	}

	return errors.Join(errs...)
}

// Client returns a mailbox client for the given user.
func (s *service) Client(userID string) Mailbox {
	return &userMailbox{
		userID:      userID,
		service:     s,
		validUserID: isValidUserID(userID),
	}
}

// isValidUserID checks if a user ID is valid.
// Valid user IDs are non-empty and contain only safe characters.
// This prevents cache key injection and other security issues.
func isValidUserID(userID string) bool {
	if userID == "" {
		return false
	}
	// Allow alphanumeric, hyphen, underscore, period, at-sign
	// Disallow: *, :, /, \, spaces, and control characters
	for _, c := range userID {
		if c == '*' || c == ':' || c == '/' || c == '\\' ||
			c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
			c < 32 || c == 127 {
			return false
		}
	}
	return true
}

// CleanupTrashResult contains the result of a trash cleanup operation.
type CleanupTrashResult struct {
	// DeletedCount is the number of messages permanently deleted.
	DeletedCount int
	// Interrupted indicates if the cleanup was interrupted (e.g., context cancelled).
	Interrupted bool
}

// CleanupTrash permanently deletes messages that have been in trash longer than
// the configured retention period (default 30 days).
//
// This method processes all expired trash messages in batches until complete
// or the context is cancelled. It uses the store's atomic DeleteExpiredTrash
// operation for efficient bulk deletion.
//
// This method should be called periodically by the application using its own
// scheduler (e.g., cron job, background worker). The library does not
// automatically run cleanup to give applications full control over scheduling.
//
// Example with a simple ticker:
//
//	go func() {
//	    ticker := time.NewTicker(1 * time.Hour)
//	    defer ticker.Stop()
//	    for range ticker.C {
//	        result, err := svc.CleanupTrash(ctx)
//	        if err != nil {
//	            log.Printf("trash cleanup error: %v", err)
//	        } else if result.DeletedCount > 0 {
//	            log.Printf("cleaned up %d expired trash messages", result.DeletedCount)
//	        }
//	    }
//	}()
func (s *service) CleanupTrash(ctx context.Context) (*CleanupTrashResult, error) {
	if atomic.LoadInt32(&s.state) != stateConnected {
		return nil, ErrNotConnected
	}

	result := &CleanupTrashResult{}
	cutoff := time.Now().UTC().Add(-s.opts.trashRetention)

	// Process in batches until no more expired messages
	const batchSize = 100
	for {
		// Check if context was cancelled
		if ctx.Err() != nil {
			result.Interrupted = true
			return result, ctx.Err()
		}

		// Step 1: Find expired trash messages to collect attachment IDs before deletion.
		// Note: There is a small race window between this query and the delete in Step 3.
		// Messages restored or newly trashed between steps may cause attachment orphans.
		// This is acceptable: orphaned attachments are harmless (wasted storage) while
		// prematurely deleted attachments would cause data loss.
		filters := []store.Filter{
			store.InFolder(store.FolderTrash),
		}
		updatedBeforeFilter, err := store.MessageFilter("UpdatedAt").LessThan(cutoff)
		if err != nil {
			return result, fmt.Errorf("create trash filter: %w", err)
		}
		filters = append(filters, updatedBeforeFilter)

		list, err := s.store.Find(ctx, filters, store.ListOptions{Limit: batchSize})
		if err != nil {
			return result, fmt.Errorf("find expired trash: %w", err)
		}

		// No more messages to delete
		if len(list.Messages) == 0 {
			break
		}

		// Step 2: Collect attachment IDs for cleanup after deletion
		var attachmentIDs []string
		if s.attachments != nil {
			for _, msg := range list.Messages {
				for _, a := range msg.GetAttachments() {
					attachmentIDs = append(attachmentIDs, a.GetID())
				}
			}
		}

		// Step 3: Use atomic bulk deletion via DeleteExpiredTrash
		deleted, err := s.store.DeleteExpiredTrash(ctx, cutoff)
		if err != nil {
			return result, fmt.Errorf("delete expired trash: %w", err)
		}

		result.DeletedCount += int(deleted)
		s.logger.Debug("deleted expired trash batch", "count", deleted)

		// Step 4: Release attachment references after successful deletion
		// If this fails, we have orphaned attachments (better than missing attachments)
		if s.attachments != nil {
			for _, attachmentID := range attachmentIDs {
				if err := s.attachments.RemoveRef(ctx, attachmentID); err != nil {
					s.logger.Warn("failed to release attachment ref during cleanup",
						"error", err, "attachment_id", attachmentID)
				}
			}
		}

		// If we deleted fewer than batch size, we're done
		if deleted < batchSize {
			break
		}
	}

	return result, nil
}

// userMailbox is the default implementation of Mailbox.
type userMailbox struct {
	userID      string
	service     *service
	validUserID bool // set by Client() after validation
}

// UserID returns the user ID of this mailbox.
func (m *userMailbox) UserID() string {
	return m.userID
}

// isConnected checks if the service is connected.
func (m *userMailbox) isConnected() bool {
	return atomic.LoadInt32(&m.service.state) == stateConnected
}

// checkAccess verifies the mailbox is ready for operations.
// Returns ErrNotConnected if service isn't connected,
// or ErrInvalidUserID if user ID failed validation.
func (m *userMailbox) checkAccess() error {
	if !m.isConnected() {
		return ErrNotConnected
	}
	if !m.validUserID {
		return ErrInvalidUserID
	}
	return nil
}

// Compose starts a new message draft.
func (m *userMailbox) Compose() (Draft, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}
	return newDraft(m), nil
}

// createSenderMessage creates the sender's copy in the outbox.
// Returns the created message or an error. Handles attachment ref counting and rollback.
func (m *userMailbox) createSenderMessage(ctx context.Context, draft store.DraftMessage, threadID, replyToID string) (store.Message, error) {
	senderData := store.MessageData{
		OwnerID:      m.userID,
		SenderID:     m.userID,
		RecipientIDs: draft.GetRecipientIDs(),
		Subject:      draft.GetSubject(),
		Body:         draft.GetBody(),
		Metadata:     draft.GetMetadata(),
		Status:       store.MessageStatusQueued,
		FolderID:     store.FolderOutbox,
		Attachments:  draft.GetAttachments(),
		ThreadID:     threadID,
		ReplyToID:    replyToID,
	}

	senderCopy, err := m.service.store.CreateMessage(ctx, senderData)
	if err != nil {
		return nil, fmt.Errorf("create sender message: %w", err)
	}

	// Increment attachment refs for sender copy.
	// If this fails, we must rollback the sender copy to avoid inconsistent state.
	if err := m.addAttachmentRefs(ctx, senderCopy); err != nil {
		m.service.logger.Error("failed to add attachment refs for sender copy - rolling back",
			"error", err, "message_id", senderCopy.GetID())

		// Try to release any refs that were partially added
		if releaseErr := m.releaseAttachmentRefs(ctx, senderCopy); releaseErr != nil {
			m.service.logger.Error("failed to release partial attachment refs during rollback",
				"error", releaseErr, "message_id", senderCopy.GetID())
		}

		// Rollback: delete the sender copy since attachment refs are inconsistent
		if deleteErr := m.service.store.HardDelete(ctx, senderCopy.GetID()); deleteErr != nil {
			m.service.logger.Error("CRITICAL: failed to rollback sender message after attachment ref failure - orphaned message",
				"error", deleteErr, "message_id", senderCopy.GetID())
			// Return combined error so caller knows rollback failed
			return nil, fmt.Errorf("add attachment refs failed and rollback failed (orphaned message %s): %w",
				senderCopy.GetID(), errors.Join(err, deleteErr))
		}
		return nil, fmt.Errorf("add attachment refs: %w", err)
	}

	return senderCopy, nil
}

// deduplicateRecipients returns a list of unique recipient IDs.
func deduplicateRecipients(recipientIDs []string) []string {
	seen := make(map[string]bool, len(recipientIDs))
	unique := make([]string, 0, len(recipientIDs))
	for _, id := range recipientIDs {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	return unique
}

// deliverToRecipients creates message copies for all unique recipients.
// senderMsgID is used as the idempotency base to prevent duplicates on retry.
// Returns lists of successful and failed recipients.
func (m *userMailbox) deliverToRecipients(ctx context.Context, draft store.DraftMessage, threadID, replyToID, senderMsgID string) ([]string, map[string]error) {
	// Recipients are already deduplicated in sendDraft before validation.
	// Use sender message ID as idempotency base.
	// This ensures retries after partial delivery don't create duplicates,
	// while allowing multiple sends with the same content to create separate messages.
	idempotencyBase := senderMsgID

	var deliveredTo []string
	failedRecipients := make(map[string]error)

	// Create message for each recipient with idempotency
	for _, recipientID := range draft.GetRecipientIDs() {
		data := store.MessageData{
			OwnerID:      recipientID,
			SenderID:     m.userID,
			RecipientIDs: draft.GetRecipientIDs(), // Keep original list for display
			Subject:      draft.GetSubject(),
			Body:         draft.GetBody(),
			Metadata:     draft.GetMetadata(),
			Status:       store.MessageStatusDelivered,
			FolderID:     store.FolderInbox,
			Attachments:  draft.GetAttachments(),
			ThreadID:     threadID,
			ReplyToID:    replyToID,
		}

		// Use idempotent create to handle retries after partial delivery
		idempotencyKey := fmt.Sprintf("%s:%s", idempotencyBase, recipientID)
		recipientCopy, created, err := m.service.store.CreateMessageIdempotent(ctx, data, idempotencyKey)
		if err != nil {
			failedRecipients[recipientID] = fmt.Errorf("create message: %w", err)
			continue
		}

		// Only add attachment refs for newly created messages
		if created {
			if refErr := m.addAttachmentRefs(ctx, recipientCopy); refErr != nil {
				// Attachment ref failure means the message won't be fully functional.
				// Delete the message and mark as failed to maintain consistency.
				if deleteErr := m.service.store.HardDelete(ctx, recipientCopy.GetID()); deleteErr != nil {
					m.service.logger.Error("failed to rollback recipient message after attachment ref failure",
						"error", deleteErr, "message_id", recipientCopy.GetID(), "recipient", recipientID)
				}
				failedRecipients[recipientID] = fmt.Errorf("add attachment refs: %w", refErr)
				continue
			}
		}

		deliveredTo = append(deliveredTo, recipientID)
	}

	return deliveredTo, failedRecipients
}

// rollbackSenderMessage cleans up the sender's message copy on total delivery failure.
// Returns an error if rollback fails (caller should log but continue).
func (m *userMailbox) rollbackSenderMessage(ctx context.Context, senderCopy store.Message) error {
	var errs []error

	if err := m.releaseAttachmentRefs(ctx, senderCopy); err != nil {
		errs = append(errs, fmt.Errorf("release attachment refs: %w", err))
	}

	if err := m.service.store.HardDelete(ctx, senderCopy.GetID()); err != nil {
		errs = append(errs, fmt.Errorf("hard delete sender message: %w", err))
	}

	return errors.Join(errs...)
}

// finalizeDelivery handles post-delivery tasks: move to sent, draft cleanup, event publishing.
// Returns the message and any error. If event publishing fails with eventErrorsFatal=true,
// returns both the message AND an EventPublishError so the caller knows the message was sent.
func (m *userMailbox) finalizeDelivery(ctx context.Context, senderCopy store.Message, deliveredTo []string, draft store.DraftMessage, sentAt time.Time) (store.Message, error) {
	// Move sender's copy to sent folder
	if err := m.service.store.MoveToFolder(ctx, senderCopy.GetID(), store.FolderSent); err != nil {
		return nil, fmt.Errorf("move message to sent folder: %w", err)
	}

	// Delete the draft if it was saved
	if draft.GetID() != "" {
		if err := m.service.store.DeleteDraft(ctx, draft.GetID()); err != nil {
			return nil, fmt.Errorf("delete draft after send: %w", err)
		}
	}

	// Re-fetch to get updated folder
	updatedCopy, err := m.service.store.Get(ctx, senderCopy.GetID())
	if err != nil {
		return nil, fmt.Errorf("fetch updated sender copy: %w", err)
	}

	// Publish event - do this AFTER we have the updated copy so we can return it even on error
	if err := m.service.events.MessageSent.Publish(ctx, MessageSentEvent{
		MessageID:    senderCopy.GetID(),
		SenderID:     senderCopy.GetSenderID(),
		RecipientIDs: deliveredTo,
		Subject:      senderCopy.GetSubject(),
		SentAt:       sentAt,
	}); err != nil {
		if m.service.opts.eventErrorsFatal {
			// Return the message WITH an error - message was sent but event failed
			return updatedCopy, &EventPublishError{
				Event:     "MessageSent",
				MessageID: senderCopy.GetID(),
				Err:       err,
			}
		}
		m.service.opts.safeEventPublishFailure("MessageSent", err)
	}

	return updatedCopy, nil
}

// sendDraft sends a draft message to recipients.
// Creates a copy of the message for the sender and each recipient.
// threadID and replyToID are optional thread context for conversation support.
func (m *userMailbox) sendDraft(ctx context.Context, draft store.DraftMessage, threadID, replyToID string) (store.Message, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// Step 1: Deduplicate recipients before validation so that the recipient count
	// check reflects the actual number of unique recipients.
	draft.SetRecipients(deduplicateRecipients(draft.GetRecipientIDs())...)

	// Step 2: Validate the draft (before acquiring semaphore to avoid wasting slots)
	if err := ValidateDraft(draft, m.service.opts.getLimits()); err != nil {
		return nil, err
	}

	// Setup tracing
	ctx, endSpan := m.service.otel.startSpan(ctx, "mailbox.send",
		attribute.String("user_id", m.userID),
		attribute.Int("recipient_count", len(draft.GetRecipientIDs())),
	)
	start := time.Now()
	var sendErr error
	defer func() {
		endSpan(sendErr)
		m.service.otel.recordSend(ctx, time.Since(start), len(draft.GetRecipientIDs()), sendErr)
	}()

	// Step 3: Acquire send semaphore
	if err := m.service.sendSem.Acquire(ctx, 1); err != nil {
		sendErr = err
		return nil, sendErr
	}
	defer m.service.sendSem.Release(1)

	// Step 4: Plugin BeforeSend hook
	if err := m.service.plugins.beforeSend(ctx, m.userID, draft); err != nil {
		sendErr = err
		return nil, sendErr
	}

	// Step 5: Create sender's copy
	senderCopy, err := m.createSenderMessage(ctx, draft, threadID, replyToID)
	if err != nil {
		sendErr = err
		return nil, sendErr
	}

	// Step 6: Deliver to recipients (use sender message ID for idempotency)
	deliveredTo, failedRecipients := m.deliverToRecipients(ctx, draft, threadID, replyToID, senderCopy.GetID())

	// Step 7: Handle total delivery failure
	if len(deliveredTo) == 0 {
		rollbackErr := m.rollbackSenderMessage(ctx, senderCopy)
		sendErr = fmt.Errorf("send failed: all %d recipients failed delivery", len(draft.GetRecipientIDs()))
		if rollbackErr != nil {
			sendErr = fmt.Errorf("%w (rollback also failed: %v)", sendErr, rollbackErr)
		}
		return nil, sendErr
	}

	// Step 8: Finalize successful delivery
	now := time.Now().UTC()
	updatedSenderCopy, eventErr := m.finalizeDelivery(ctx, senderCopy, deliveredTo, draft, now)
	if eventErr != nil {
		sendErr = eventErr
		return nil, sendErr
	}

	// Step 9: Handle partial delivery
	if len(failedRecipients) > 0 {
		sendErr = &PartialDeliveryError{
			MessageID:        updatedSenderCopy.GetID(),
			DeliveredTo:      deliveredTo,
			FailedRecipients: failedRecipients,
		}
		return updatedSenderCopy, sendErr
	}

	// Step 10: Plugin AfterSend hook
	if err := m.service.plugins.afterSend(ctx, m.userID, updatedSenderCopy); err != nil {
		sendErr = err
		return updatedSenderCopy, sendErr
	}

	return updatedSenderCopy, nil
}

// saveDraft saves a draft without sending.
func (m *userMailbox) saveDraft(ctx context.Context, draft store.DraftMessage) (store.DraftMessage, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	limits := m.service.opts.getLimits()

	// Validate content if provided (drafts can have empty subject)
	if draft.GetSubject() != "" {
		if err := ValidateSubjectWithLimits(draft.GetSubject(), limits); err != nil {
			return nil, err
		}
	}
	if draft.GetBody() != "" {
		if err := ValidateBodyWithLimits(draft.GetBody(), limits); err != nil {
			return nil, err
		}
	}
	if err := ValidateMetadataWithLimits(draft.GetMetadata(), limits); err != nil {
		return nil, err
	}
	// Validate attachments
	if err := ValidateAttachments(draft.GetAttachments(), limits); err != nil {
		return nil, err
	}

	savedDraft, err := m.service.store.SaveDraft(ctx, draft)
	if err != nil {
		return nil, fmt.Errorf("save draft: %w", err)
	}

	return savedDraft, nil
}

// Get retrieves a message by ID.
func (m *userMailbox) Get(ctx context.Context, messageID string) (Message, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// OTel tracing
	ctx, endSpan := m.service.otel.startSpan(ctx, "mailbox.get",
		attribute.String("user_id", m.userID),
		attribute.String("message_id", messageID),
	)
	start := time.Now()
	var getErr error
	defer func() {
		endSpan(getErr)
		m.service.otel.recordGet(ctx, time.Since(start), getErr)
	}()

	msg, storeErr := m.service.store.Get(ctx, messageID)
	if storeErr != nil {
		getErr = storeErr
		return nil, fmt.Errorf("get message: %w", storeErr)
	}

	if !m.canAccess(msg) {
		getErr = ErrUnauthorized
		return nil, ErrUnauthorized
	}

	return newMessage(msg, m), nil
}

// UpdateFlags updates message flags (read status, archived status).
func (m *userMailbox) UpdateFlags(ctx context.Context, messageID string, flags Flags) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	// Apply archived flag first (moves to/from archived folder).
	// This is the more complex operation and is done first so that if it fails,
	// no rollback of the read flag is needed.
	if flags.Archived != nil {
		var folderID string
		if *flags.Archived {
			folderID = store.FolderArchived
		} else {
			// Restore to inbox for received messages, sent for sent messages
			if store.IsSentByOwner(msg.GetOwnerID(), msg.GetSenderID()) {
				folderID = store.FolderSent
			} else {
				folderID = store.FolderInbox
			}
		}
		if err := m.service.store.MoveToFolder(ctx, messageID, folderID); err != nil {
			return fmt.Errorf("move to folder: %w", err)
		}
	}

	// Apply read flag after archive (less severe failure mode if this fails)
	if flags.Read != nil {
		if err := m.service.store.MarkRead(ctx, messageID, *flags.Read); err != nil {
			return fmt.Errorf("mark read: %w", err)
		}
	}

	// Publish read event (only for marking as read, not unread)
	if flags.Read != nil && *flags.Read {
		if err := m.service.events.MessageRead.Publish(ctx, MessageReadEvent{
			MessageID: messageID,
			UserID:    m.userID,
			ReadAt:    time.Now().UTC(),
		}); err != nil {
			if m.service.opts.eventErrorsFatal {
				// Operation succeeded but event failed - return EventPublishError
				return &EventPublishError{
					Event:     "MessageRead",
					MessageID: messageID,
					Err:       err,
				}
			}
			m.service.opts.safeEventPublishFailure("MessageRead", err)
		}
	}

	return nil
}

// Delete moves a message to trash.
func (m *userMailbox) Delete(ctx context.Context, messageID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if msg.GetFolderID() == store.FolderTrash {
		return ErrAlreadyInTrash
	}

	if err := m.service.store.MoveToFolder(ctx, messageID, store.FolderTrash); err != nil {
		return fmt.Errorf("move to trash: %w", err)
	}

	return nil
}

// Restore restores a message from trash.
func (m *userMailbox) Restore(ctx context.Context, messageID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if msg.GetFolderID() != store.FolderTrash {
		return ErrNotInTrash
	}

	// Restore to inbox for received messages, sent for sent messages
	var folderID string
	if store.IsSentByOwner(msg.GetOwnerID(), msg.GetSenderID()) {
		folderID = store.FolderSent
	} else {
		folderID = store.FolderInbox
	}

	if err := m.service.store.MoveToFolder(ctx, messageID, folderID); err != nil {
		return fmt.Errorf("restore message: %w", err)
	}

	return nil
}

// PermanentlyDelete permanently deletes a message from trash.
func (m *userMailbox) PermanentlyDelete(ctx context.Context, messageID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if msg.GetFolderID() != store.FolderTrash {
		return ErrNotInTrash
	}

	// Hard delete the message FIRST to avoid race condition.
	// If we released attachment refs first and then delete failed,
	// another process could see refs=0 and delete the attachments
	// while the message still exists.
	if err := m.service.store.HardDelete(ctx, messageID); err != nil {
		return fmt.Errorf("hard delete message: %w", err)
	}

	// Release attachment references AFTER successful delete.
	// If this fails, we have orphaned attachments (better than missing attachments on existing messages).
	if err := m.releaseAttachmentRefs(ctx, msg); err != nil {
		m.service.logger.Error("failed to release attachment refs during permanent delete - attachments may be orphaned",
			"error", err, "message_id", messageID)
	}

	// Publish event
	if err := m.service.events.MessageDeleted.Publish(ctx, MessageDeletedEvent{
		MessageID: messageID,
		UserID:    m.userID,
		DeletedAt: time.Now().UTC(),
	}); err != nil {
		if m.service.opts.eventErrorsFatal {
			// Operation succeeded but event failed - return EventPublishError
			return &EventPublishError{
				Event:     "MessageDeleted",
				MessageID: messageID,
				Err:       err,
			}
		}
		m.service.opts.safeEventPublishFailure("MessageDeleted", err)
	}

	return nil
}

// MoveToFolder moves a message to a folder.
func (m *userMailbox) MoveToFolder(ctx context.Context, messageID, folderID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	// Validate folder ID
	if !store.IsValidFolderID(folderID) {
		return fmt.Errorf("%w: %s", ErrInvalidFolderID, folderID)
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if err := m.service.store.MoveToFolder(ctx, messageID, folderID); err != nil {
		return fmt.Errorf("move to folder: %w", err)
	}

	return nil
}

// MaxTagIDLength is the maximum length for a tag ID.
const MaxTagIDLength = 256

// AddTag adds a tag to a message.
func (m *userMailbox) AddTag(ctx context.Context, messageID, tagID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	if tagID == "" {
		return fmt.Errorf("%w: empty tag ID", ErrInvalidID)
	}
	if len(tagID) > MaxTagIDLength {
		return fmt.Errorf("%w: tag ID exceeds maximum length of %d", ErrInvalidID, MaxTagIDLength)
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if err := m.service.store.AddTag(ctx, messageID, tagID); err != nil {
		return fmt.Errorf("add tag: %w", err)
	}

	return nil
}

// RemoveTag removes a tag from a message.
func (m *userMailbox) RemoveTag(ctx context.Context, messageID, tagID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	if tagID == "" {
		return fmt.Errorf("%w: empty tag ID", ErrInvalidID)
	}
	if len(tagID) > MaxTagIDLength {
		return fmt.Errorf("%w: tag ID exceeds maximum length of %d", ErrInvalidID, MaxTagIDLength)
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if err := m.service.store.RemoveTag(ctx, messageID, tagID); err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}

	return nil
}

// Inbox returns messages in the user's inbox (received messages, not archived or trashed).
func (m *userMailbox) Inbox(ctx context.Context, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "inbox", opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(store.FolderInbox),
			store.NotDeleted(),
		}
	})
}

// Sent returns messages sent by the user.
func (m *userMailbox) Sent(ctx context.Context, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "sent", opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(store.FolderSent),
			store.NotDeleted(),
		}
	})
}

// Archived returns archived messages for the current user.
func (m *userMailbox) Archived(ctx context.Context, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "archived", opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(store.FolderArchived),
			store.NotDeleted(),
		}
	})
}

// Trash returns messages in trash for the current user.
func (m *userMailbox) Trash(ctx context.Context, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "trash", opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(store.FolderTrash),
		}
	})
}

// Drafts returns draft messages for the current user.
func (m *userMailbox) Drafts(ctx context.Context, opts store.ListOptions) (DraftList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	storeDrafts, err := m.service.store.ListDrafts(ctx, m.userID, opts)
	if err != nil {
		return nil, fmt.Errorf("list drafts: %w", err)
	}

	drafts := make([]Draft, len(storeDrafts.Drafts))
	for i, d := range storeDrafts.Drafts {
		drafts[i] = &draft{
			mailbox: m,
			message: d,
			saved:   true,
		}
	}

	return &draftList{
		mailbox:    m,
		drafts:     drafts,
		total:      storeDrafts.Total,
		hasMore:    storeDrafts.HasMore,
		nextCursor: storeDrafts.NextCursor,
	}, nil
}

// draftList is the internal implementation of DraftList.
type draftList struct {
	mailbox    *userMailbox
	drafts     []Draft
	total      int64
	hasMore    bool
	nextCursor string
}

func (l *draftList) All() []Draft       { return l.drafts }
func (l *draftList) Total() int64       { return l.total }
func (l *draftList) HasMore() bool      { return l.hasMore }
func (l *draftList) NextCursor() string { return l.nextCursor }

func (l *draftList) IDs() []string {
	ids := make([]string, 0, len(l.drafts))
	for _, d := range l.drafts {
		if id := d.ID(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// Delete deletes all drafts in this list.
func (l *draftList) Delete(ctx context.Context) (*BulkResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.drafts))}

	for _, draft := range l.drafts {
		if draft.ID() == "" {
			continue // Skip unsaved drafts
		}
		res := OperationResult{ID: draft.ID()}
		if err := l.mailbox.service.store.DeleteDraft(ctx, draft.ID()); err != nil {
			res.Error = err
		} else {
			res.Success = true
		}
		result.Results = append(result.Results, res)
	}

	return result, result.Err()
}

// Send sends all drafts in this list.
func (l *draftList) Send(ctx context.Context) (*BulkResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.drafts))}

	for _, draft := range l.drafts {
		draftID := draft.ID()
		if draftID == "" {
			draftID = "unsaved-draft"
		}
		res := OperationResult{ID: draftID}
		msg, err := draft.Send(ctx)
		if err != nil {
			res.Error = err
		} else {
			res.Success = true
			res.Message = msg
		}
		result.Results = append(result.Results, res)
	}

	return result, result.Err()
}

// Folder returns messages in a specific folder for the current user.
func (m *userMailbox) Folder(ctx context.Context, folderID string, opts store.ListOptions) (MessageList, error) {
	return m.listWithOTel(ctx, "folder:"+folderID, opts, func() []store.Filter {
		return []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(folderID),
			store.NotDeleted(),
		}
	})
}

// listWithOTel is a helper that adds OTel instrumentation to list operations.
func (m *userMailbox) listWithOTel(ctx context.Context, folder string, opts store.ListOptions, getFilters func() []store.Filter) (MessageList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// OTel tracing
	ctx, endSpan := m.service.otel.startSpan(ctx, "mailbox.list",
		attribute.String("user_id", m.userID),
		attribute.String("folder", folder),
	)
	start := time.Now()
	var listErr error
	var resultCount int
	defer func() {
		endSpan(listErr)
		m.service.otel.recordList(ctx, time.Since(start), folder, resultCount)
	}()

	filters := getFilters()
	storeList, err := m.listMessages(ctx, filters, opts)
	if err != nil {
		listErr = err
		return nil, err
	}
	resultCount = len(storeList.Messages)

	return wrapMessageList(storeList, m), nil
}

// Search searches messages accessible to the current user.
func (m *userMailbox) Search(ctx context.Context, query SearchQuery) (MessageList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// OTel tracing
	ctx, endSpan := m.service.otel.startSpan(ctx, "mailbox.search",
		attribute.String("user_id", m.userID),
		attribute.String("query", query.Query),
	)
	start := time.Now()
	var searchErr error
	defer func() {
		endSpan(searchErr)
		m.service.otel.recordSearch(ctx, time.Since(start), 0)
	}()

	// Apply default query limit if not specified
	if query.Options.Limit == 0 {
		query.Options.Limit = m.service.opts.defaultQueryLimit
	}
	// Enforce maximum query limit to prevent resource exhaustion
	if query.Options.Limit > m.service.opts.maxQueryLimit {
		query.Options.Limit = m.service.opts.maxQueryLimit
	}

	// Set owner for search
	query.OwnerID = m.userID

	// Add filter to only search user's messages
	query.Filters = append(query.Filters,
		store.OwnerIs(m.userID),
		store.NotDeleted(),
	)

	// Add tag filters if specified
	if len(query.Tags) > 0 {
		query.Filters = append(query.Filters, store.HasTags(query.Tags)...)
	}

	storeList, err := m.service.store.Search(ctx, query)
	if err != nil {
		searchErr = err
		return nil, fmt.Errorf("search messages: %w", err)
	}


	return wrapMessageList(storeList, m), nil
}

// systemFolders defines the standard system folders with their display names.
var systemFolders = []struct {
	ID   string
	Name string
}{
	{store.FolderInbox, "Inbox"},
	{store.FolderSent, "Sent"},
	{store.FolderArchived, "Archived"},
	{store.FolderTrash, "Trash"},
}

// systemFolderSet is used to quickly check if a folder ID is a system folder.
var systemFolderSet = func() map[string]bool {
	m := make(map[string]bool, len(systemFolders))
	for _, sf := range systemFolders {
		m[sf.ID] = true
	}
	return m
}()

// ListFolders returns information about all folders for the current user.
// Always includes system folders. If the store implements store.FolderLister,
// custom (non-system) folders are included as well.
// If the store implements store.FolderCounter, batch counting is used.
func (m *userMailbox) ListFolders(ctx context.Context) ([]FolderInfo, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// Discover all folder IDs (system + custom if store supports it).
	allFolderIDs := make([]string, len(systemFolders))
	for i, sf := range systemFolders {
		allFolderIDs[i] = sf.ID
	}

	var customFolderIDs []string
	if fl, ok := m.service.store.(store.FolderLister); ok {
		distinct, err := fl.ListDistinctFolders(ctx, m.userID)
		if err != nil {
			return nil, fmt.Errorf("list distinct folders: %w", err)
		}
		for _, id := range distinct {
			if !systemFolderSet[id] {
				customFolderIDs = append(customFolderIDs, id)
				allFolderIDs = append(allFolderIDs, id)
			}
		}
	}

	// Fast path: use batch counting if the store supports it.
	if fc, ok := m.service.store.(store.FolderCounter); ok {
		counts, err := fc.CountByFolders(ctx, m.userID, allFolderIDs)
		if err != nil {
			return nil, fmt.Errorf("count folders: %w", err)
		}

		folders := make([]FolderInfo, 0, len(allFolderIDs))
		for _, sf := range systemFolders {
			c := counts[sf.ID]
			folders = append(folders, FolderInfo{
				ID:           sf.ID,
				Name:         sf.Name,
				IsSystem:     true,
				MessageCount: c.Total,
				UnreadCount:  c.Unread,
			})
		}
		for _, id := range customFolderIDs {
			c := counts[id]
			folders = append(folders, FolderInfo{
				ID:           id,
				Name:         id,
				MessageCount: c.Total,
				UnreadCount:  c.Unread,
			})
		}
		return folders, nil
	}

	// Slow path: individual Count queries per folder.
	folders := make([]FolderInfo, 0, len(allFolderIDs))

	for _, id := range allFolderIDs {
		count, err := m.service.store.Count(ctx, []store.Filter{
			store.OwnerIs(m.userID),
			store.NotDeleted(),
			store.InFolder(id),
		})
		if err != nil {
			return nil, fmt.Errorf("count folder %s: %w", id, err)
		}

		unreadCount, err := m.service.store.Count(ctx, []store.Filter{
			store.OwnerIs(m.userID),
			store.NotDeleted(),
			store.InFolder(id),
			store.IsReadFilter(false),
		})
		if err != nil {
			return nil, fmt.Errorf("count unread in folder %s: %w", id, err)
		}

		isSystem := systemFolderSet[id]
		name := id
		if isSystem {
			for _, sf := range systemFolders {
				if sf.ID == id {
					name = sf.Name
					break
				}
			}
		}

		folders = append(folders, FolderInfo{
			ID:           id,
			Name:         name,
			IsSystem:     isSystem,
			MessageCount: count,
			UnreadCount:  unreadCount,
		})
	}

	return folders, nil
}

// GetThread returns all messages in a thread, ordered by creation time.
// Only returns messages owned by the current user.
func (m *userMailbox) GetThread(ctx context.Context, threadID string, opts store.ListOptions) (MessageList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	if threadID == "" {
		return nil, fmt.Errorf("%w: empty thread ID", ErrInvalidID)
	}

	// Set default sorting to creation time ascending for threads
	if opts.SortBy == "" {
		opts.SortBy = "created_at"
		opts.SortOrder = store.SortAsc
	}

	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.NotDeleted(),
		store.ThreadIs(threadID),
	}

	storeList, err := m.service.store.Find(ctx, filters, opts)
	if err != nil {
		return nil, fmt.Errorf("get thread: %w", err)
	}

	return wrapMessageList(storeList, m), nil
}

// GetReplies returns all direct replies to a message.
// Only returns replies owned by the current user.
func (m *userMailbox) GetReplies(ctx context.Context, messageID string, opts store.ListOptions) (MessageList, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	if messageID == "" {
		return nil, fmt.Errorf("%w: empty message ID", ErrInvalidID)
	}

	// Set default sorting to creation time ascending
	if opts.SortBy == "" {
		opts.SortBy = "created_at"
		opts.SortOrder = store.SortAsc
	}

	filters := []store.Filter{
		store.OwnerIs(m.userID),
		store.NotDeleted(),
		store.ReplyToIs(messageID),
	}

	storeList, err := m.service.store.Find(ctx, filters, opts)
	if err != nil {
		return nil, fmt.Errorf("get replies: %w", err)
	}

	return wrapMessageList(storeList, m), nil
}

// LoadAttachment loads attachment content by message and attachment ID.
func (m *userMailbox) LoadAttachment(ctx context.Context, messageID, attachmentID string) (io.ReadCloser, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	if m.service.attachments == nil {
		return nil, ErrAttachmentStoreNotConfigured
	}

	// Get the message to verify access and find attachment
	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return nil, ErrUnauthorized
	}

	// Verify the attachment belongs to this message
	attachments := msg.GetAttachments()
	var found bool
	for _, a := range attachments {
		if a.GetID() == attachmentID {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrAttachmentNotFound
	}

	// Load content from attachment manager
	return m.service.attachments.Load(ctx, attachmentID)
}

// SendMessage sends a message directly without going through the draft flow.
// If AttachmentIDs are provided, they are resolved via ResolveAttachments
// and merged with any Attachments already in the request.
func (m *userMailbox) SendMessage(ctx context.Context, req SendRequest) (Message, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	// Resolve attachment IDs if any
	allAttachments := req.Attachments
	if len(req.AttachmentIDs) > 0 {
		resolved, err := m.ResolveAttachments(ctx, req.AttachmentIDs)
		if err != nil {
			return nil, fmt.Errorf("resolve attachments: %w", err)
		}
		allAttachments = append(allAttachments, resolved...)
	}

	// Build a transient draft
	draft := m.service.store.NewDraft(m.userID)
	draft.SetRecipients(req.RecipientIDs...)
	draft.SetSubject(req.Subject)
	draft.SetBody(req.Body)
	for k, v := range req.Metadata {
		draft.SetMetadata(k, v)
	}
	for _, a := range allAttachments {
		draft.AddAttachment(a)
	}

	// Send via existing flow
	msg, err := m.sendDraft(ctx, draft, req.ThreadID, req.ReplyToID)
	if err != nil {
		return nil, err
	}

	return newMessage(msg, m), nil
}

// BulkUpdateFlags updates flags on multiple messages by ID.
func (m *userMailbox) BulkUpdateFlags(ctx context.Context, messageIDs []string, flags Flags) (*BulkResult, error) {
	return m.bulkOp(messageIDs, func(id string) error {
		return m.UpdateFlags(ctx, id, flags)
	})
}

// BulkMove moves multiple messages to a folder by ID.
func (m *userMailbox) BulkMove(ctx context.Context, messageIDs []string, folderID string) (*BulkResult, error) {
	return m.bulkOp(messageIDs, func(id string) error {
		return m.MoveToFolder(ctx, id, folderID)
	})
}

// BulkDelete moves multiple messages to trash by ID.
func (m *userMailbox) BulkDelete(ctx context.Context, messageIDs []string) (*BulkResult, error) {
	return m.bulkOp(messageIDs, func(id string) error {
		return m.Delete(ctx, id)
	})
}

// BulkAddTag adds a tag to multiple messages by ID.
func (m *userMailbox) BulkAddTag(ctx context.Context, messageIDs []string, tagID string) (*BulkResult, error) {
	return m.bulkOp(messageIDs, func(id string) error {
		return m.AddTag(ctx, id, tagID)
	})
}

// BulkRemoveTag removes a tag from multiple messages by ID.
func (m *userMailbox) BulkRemoveTag(ctx context.Context, messageIDs []string, tagID string) (*BulkResult, error) {
	return m.bulkOp(messageIDs, func(id string) error {
		return m.RemoveTag(ctx, id, tagID)
	})
}

// bulkOp applies an operation to each message ID, collecting results.
func (m *userMailbox) bulkOp(messageIDs []string, op func(id string) error) (*BulkResult, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	result := &BulkResult{Results: make([]OperationResult, 0, len(messageIDs))}
	for _, id := range messageIDs {
		res := OperationResult{ID: id}
		if err := op(id); err != nil {
			res.Error = err
		} else {
			res.Success = true
		}
		result.Results = append(result.Results, res)
	}
	return result, result.Err()
}

// ResolveAttachments resolves attachment metadata by IDs.
// Returns attachment metadata for each ID in order.
func (m *userMailbox) ResolveAttachments(ctx context.Context, attachmentIDs []string) ([]store.Attachment, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	if m.service.attachments == nil {
		return nil, ErrAttachmentStoreNotConfigured
	}

	attachments := make([]store.Attachment, 0, len(attachmentIDs))
	for _, id := range attachmentIDs {
		meta, err := m.service.attachments.GetMetadata(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve attachment %s: %w", id, err)
		}
		attachments = append(attachments, meta)
	}

	return attachments, nil
}

// Helper methods

func (m *userMailbox) canAccess(msg store.Message) bool {
	return msg.GetOwnerID() == m.userID
}

func (m *userMailbox) listMessages(ctx context.Context, filters []store.Filter, opts store.ListOptions) (*store.MessageList, error) {
	// Apply default query limit if not specified
	if opts.Limit == 0 {
		opts.Limit = m.service.opts.defaultQueryLimit
	}
	// Enforce maximum query limit to prevent resource exhaustion
	if opts.Limit > m.service.opts.maxQueryLimit {
		opts.Limit = m.service.opts.maxQueryLimit
	}
	if opts.SortBy == "" {
		opts.SortBy = "CreatedAt"
		opts.SortOrder = store.SortDesc
	}

	// Fast path: use combined find+count if the store supports it.
	var list *store.MessageList
	var total int64
	if fwc, ok := m.service.store.(store.FindWithCounter); ok {
		var err error
		list, total, err = fwc.FindWithCount(ctx, filters, opts)
		if err != nil {
			return nil, fmt.Errorf("find messages: %w", err)
		}
	} else {
		var err error
		list, err = m.service.store.Find(ctx, filters, opts)
		if err != nil {
			return nil, fmt.Errorf("find messages: %w", err)
		}
		total, err = m.service.store.Count(ctx, filters)
		if err != nil {
			return nil, fmt.Errorf("count messages: %w", err)
		}
	}

	var nextCursor string
	if list.HasMore && len(list.Messages) > 0 {
		nextCursor = list.Messages[len(list.Messages)-1].GetID()
	}

	return &store.MessageList{
		Messages:   list.Messages,
		Total:      total,
		HasMore:    list.HasMore,
		NextCursor: nextCursor,
	}, nil
}

// addAttachmentRefs increments reference counts for all attachments in a message.
// On failure, it rolls back any refs that were successfully added.
func (m *userMailbox) addAttachmentRefs(ctx context.Context, msg store.Message) error {
	if m.service.attachments == nil {
		return nil
	}

	attachments := msg.GetAttachments()
	added := make([]string, 0, len(attachments))

	for _, a := range attachments {
		if err := m.service.attachments.AddRef(ctx, a.GetID()); err != nil {
			m.service.logger.Warn("failed to add attachment ref", "error", err, "attachment_id", a.GetID())
			// Rollback: release refs for attachments we already added
			var rollbackFailed map[string]error
			for _, addedID := range added {
				if releaseErr := m.service.attachments.RemoveRef(ctx, addedID); releaseErr != nil {
					m.service.logger.Warn("failed to rollback attachment ref", "error", releaseErr, "attachment_id", addedID)
					if rollbackFailed == nil {
						rollbackFailed = make(map[string]error)
					}
					rollbackFailed[addedID] = releaseErr
				}
			}
			return &AttachmentRefError{
				Operation:      "add",
				Failed:         map[string]error{a.GetID(): err},
				RollbackFailed: rollbackFailed,
			}
		}
		added = append(added, a.GetID())
	}
	return nil
}

// releaseAttachmentRefs decrements reference counts for all attachments in a message.
// Returns an error if any ref releases fail (but continues processing all).
func (m *userMailbox) releaseAttachmentRefs(ctx context.Context, msg store.Message) error {
	if m.service.attachments == nil {
		return nil
	}
	var failed map[string]error
	for _, a := range msg.GetAttachments() {
		if err := m.service.attachments.RemoveRef(ctx, a.GetID()); err != nil {
			m.service.logger.Warn("failed to release attachment ref", "error", err, "attachment_id", a.GetID())
			if failed == nil {
				failed = make(map[string]error)
			}
			failed[a.GetID()] = err
		}
	}
	if len(failed) > 0 {
		return &AttachmentRefError{Operation: "release", Failed: failed}
	}
	return nil
}
