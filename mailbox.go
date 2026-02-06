package mailbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event/v3/transport/noop"
	eventredis "github.com/rbaliyan/event/v3/transport/redis"
	"github.com/rbaliyan/mailbox/store"
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
	StatsReader
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
	eventBus         *event.Bus     // Event bus for publishing events
	events           *ServiceEvents // Per-service event instances
	statsCache       sync.Map      // map[ownerID string]*statsEntry
	statsCacheEnabled bool         // true when event transport is configured
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
		s.statsCacheEnabled = true
	case s.opts.redisClient != nil:
		s.logger.Info("initializing event bus with Redis transport")
		t, transportErr := eventredis.New(s.opts.redisClient)
		if transportErr != nil {
			return fmt.Errorf("create redis transport: %w", transportErr)
		}
		bus, err = event.NewBus(busName, event.WithTransport(t))
		s.statsCacheEnabled = true
	default:
		s.logger.Debug("initializing event bus with noop transport (stats cache disabled)")
		bus, err = event.NewBus(busName, event.WithTransport(noop.New()))
		s.statsCacheEnabled = false
	}

	if err != nil {
		return fmt.Errorf("create event bus: %w", err)
	}

	// Don't assign bus to s.eventBus until all setup succeeds.
	// On failure, bus.Close() is called and s.eventBus remains nil,
	// preventing later code from seeing a partially-initialized closed bus.
	events := newServiceEvents(busName)

	// Register per-service events (unique per service instance).
	if err := registerServiceEvents(ctx, bus, events); err != nil {
		bus.Close(ctx)
		return fmt.Errorf("register service events: %w", err)
	}

	// Also register global events for backward compatibility.
	// Global events use "first registration wins" - subsequent calls are no-ops.
	if err := registerEvents(ctx, bus); err != nil {
		bus.Close(ctx)
		return fmt.Errorf("register events: %w", err)
	}

	// Subscribe internal handlers for stats cache updates.
	if err := events.MessageSent.Subscribe(ctx, s.onMessageSent); err != nil {
		bus.Close(ctx)
		return fmt.Errorf("subscribe stats MessageSent: %w", err)
	}
	if err := events.MessageRead.Subscribe(ctx, s.onMessageRead); err != nil {
		bus.Close(ctx)
		return fmt.Errorf("subscribe stats MessageRead: %w", err)
	}
	if err := events.MessageDeleted.Subscribe(ctx, s.onMessageDeleted); err != nil {
		bus.Close(ctx)
		return fmt.Errorf("subscribe stats MessageDeleted: %w", err)
	}

	// All setup succeeded - commit to service state.
	s.eventBus = bus
	s.events = events
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
// Connection state is checked lazily on each operation, not at creation time.
// If the service is not connected, operations on the returned client will
// return ErrNotConnected.
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

	// Step 1: Collect message IDs and their attachment IDs from expired trash.
	// This must happen before deletion so we know which attachment refs to release.
	// We track per-message attachment IDs so we can verify each message was actually
	// deleted before releasing its refs (prevents incorrect ref decrements if a
	// message is restored between the scan and delete steps).
	messageAttachments := make(map[string][]string) // messageID → []attachmentID
	if s.attachments != nil {
		updatedBeforeFilter, err := store.MessageFilter("UpdatedAt").LessThan(cutoff)
		if err != nil {
			return result, fmt.Errorf("create trash filter: %w", err)
		}
		filters := []store.Filter{
			store.InFolder(store.FolderTrash),
			updatedBeforeFilter,
		}

		const scanBatchSize = 100
		var cursor string
		for {
			if ctx.Err() != nil {
				result.Interrupted = true
				return result, ctx.Err()
			}

			opts := store.ListOptions{Limit: scanBatchSize, StartAfter: cursor}
			list, err := s.store.Find(ctx, filters, opts)
			if err != nil {
				return result, fmt.Errorf("find expired trash: %w", err)
			}

			for _, msg := range list.Messages {
				var ids []string
				for _, a := range msg.GetAttachments() {
					ids = append(ids, a.GetID())
				}
				if len(ids) > 0 {
					messageAttachments[msg.GetID()] = ids
				}
			}

			if !list.HasMore || len(list.Messages) == 0 {
				break
			}
			cursor = list.Messages[len(list.Messages)-1].GetID()
		}
	}

	// Step 2: Bulk delete all expired trash messages atomically.
	deleted, err := s.store.DeleteExpiredTrash(ctx, cutoff)
	if err != nil {
		return result, fmt.Errorf("delete expired trash: %w", err)
	}
	result.DeletedCount = int(deleted)
	if deleted > 0 {
		s.logger.Debug("deleted expired trash messages", "count", deleted)
	}

	// Step 3: Release attachment references only for messages confirmed deleted.
	// A message may have been restored between step 1 and step 2. Verify each
	// message no longer exists before releasing its attachment refs to prevent
	// incorrect ref decrements that could cause premature attachment deletion.
	if s.attachments != nil {
		for msgID, attIDs := range messageAttachments {
			// Verify message was actually deleted (not restored between scan and delete).
			if _, getErr := s.store.Get(ctx, msgID); getErr == nil {
				// Message still exists (was restored) - skip releasing its refs.
				s.logger.Debug("skipping attachment ref release for restored message",
					"message_id", msgID)
				continue
			}

			for _, attachmentID := range attIDs {
				if err := s.attachments.RemoveRef(ctx, attachmentID); err != nil {
					s.logger.Warn("failed to release attachment ref during cleanup",
						"error", err, "attachment_id", attachmentID, "message_id", msgID)
				}
			}
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

// UpdateFlags updates message flags (read status, archived status).
func (m *userMailbox) UpdateFlags(ctx context.Context, messageID string, flags Flags) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordUpdate(ctx, time.Since(start), "update_flags", retErr) }()

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
			FolderID:  msg.GetFolderID(),
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
func (m *userMailbox) Delete(ctx context.Context, messageID string) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordDelete(ctx, time.Since(start), false, retErr) }()

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
func (m *userMailbox) Restore(ctx context.Context, messageID string) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordUpdate(ctx, time.Since(start), "restore", retErr) }()

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
func (m *userMailbox) PermanentlyDelete(ctx context.Context, messageID string) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordDelete(ctx, time.Since(start), true, retErr) }()

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
		FolderID:  msg.GetFolderID(),
		WasUnread: !msg.GetIsRead(),
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
func (m *userMailbox) MoveToFolder(ctx context.Context, messageID, folderID string) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordMove(ctx, time.Since(start), folderID, retErr) }()

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

// Helper methods

func (m *userMailbox) canAccess(msg store.Message) bool {
	return msg.GetOwnerID() == m.userID
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
