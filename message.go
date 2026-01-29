package mailbox

import (
	"context"
	"fmt"

	"github.com/rbaliyan/mailbox/store"
)

// MessageMutator provides mutation operations on a single message.
type MessageMutator interface {
	// Update updates the message flags (read, archived status).
	// Use MarkRead(), MarkUnread(), MarkArchived(), MarkUnarchived() helpers.
	Update(ctx context.Context, flags Flags) error

	// Move moves the message to a folder.
	Move(ctx context.Context, folderID string) error

	// Delete moves the message to trash.
	Delete(ctx context.Context) error

	// Restore restores the message from trash.
	Restore(ctx context.Context) error

	// PermanentlyDelete permanently deletes the message from trash.
	PermanentlyDelete(ctx context.Context) error

	// AddTag adds a tag to the message.
	AddTag(ctx context.Context, tagID string) error

	// RemoveTag removes a tag from the message.
	RemoveTag(ctx context.Context, tagID string) error
}

// Message provides access to a message with mutation capabilities.
//
// This is the application-level message type returned by Mailbox operations.
// It wraps store.Message (the storage-level type) and adds user-scoped mutations.
// Read methods (GetID, GetSubject, etc.) come from store.Message;
// mutation methods (Update, Move, Delete, etc.) come from MessageMutator
// and are scoped to the owning user's mailbox.
//
// Important: Message is a snapshot of state at retrieval time. After mutations,
// getter methods (GetIsRead, GetFolderID, etc.) may return stale values.
// To get fresh state after mutations, call Mailbox.Get() again.
//
// Composed of:
//   - store.Message: Read-only access (GetID, GetSubject, GetBody, etc.)
//   - MessageMutator: Mutations (Update, Move, Delete, AddTag, etc.)
type Message interface {
	store.Message
	MessageMutator
}

// message is the internal implementation of Message.
// Authorization is verified once at creation time and cached.
// Getter methods return the state at creation time (or last refresh).
// To get fresh state after mutations, call Mailbox.Get() again.
type message struct {
	store.Message
	mailbox    *userMailbox
	authorized bool // set to true when authorization is verified at creation
}

// newMessage wraps a store.Message with mailbox operations.
// Authorization is verified by the caller before calling this.
func newMessage(msg store.Message, m *userMailbox) *message {
	return &message{
		Message:    msg,
		mailbox:    m,
		authorized: true, // caller verified authorization
	}
}

// Update updates the message flags.
// Skips re-authorization since this message handle was already authorized.
func (m *message) Update(ctx context.Context, flags Flags) error {
	if err := m.mailbox.checkAccess(); err != nil {
		return err
	}

	// Apply read flag
	if flags.Read != nil {
		if err := m.mailbox.service.store.MarkRead(ctx, m.GetID(), *flags.Read); err != nil {
			return fmt.Errorf("mark read: %w", err)
		}
	}

	// Apply archived flag (moves to/from archived folder)
	if flags.Archived != nil {
		var folderID string
		if *flags.Archived {
			folderID = store.FolderArchived
		} else {
			// Restore to inbox for received messages, sent for sent messages
			if store.IsSentByOwner(m.GetOwnerID(), m.GetSenderID()) {
				folderID = store.FolderSent
			} else {
				folderID = store.FolderInbox
			}
		}
		if err := m.mailbox.service.store.MoveToFolder(ctx, m.GetID(), folderID); err != nil {
			return fmt.Errorf("move to folder: %w", err)
		}
	}

	return nil
}

// Move moves the message to a folder.
// Skips re-authorization since this message handle was already authorized.
func (m *message) Move(ctx context.Context, folderID string) error {
	if err := m.mailbox.checkAccess(); err != nil {
		return err
	}

	// Validate folder ID
	if !store.IsValidFolderID(folderID) {
		return fmt.Errorf("%w: %s", ErrInvalidFolderID, folderID)
	}

	if err := m.mailbox.service.store.MoveToFolder(ctx, m.GetID(), folderID); err != nil {
		return fmt.Errorf("move to folder: %w", err)
	}

	return nil
}

// Delete moves the message to trash.
// Skips re-authorization since this message handle was already authorized.
func (m *message) Delete(ctx context.Context) error {
	if err := m.mailbox.checkAccess(); err != nil {
		return err
	}

	// Check current state - need to re-fetch to verify not already in trash
	current, err := m.mailbox.service.store.Get(ctx, m.GetID())
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if current.GetFolderID() == store.FolderTrash {
		return ErrAlreadyInTrash
	}

	if err := m.mailbox.service.store.MoveToFolder(ctx, m.GetID(), store.FolderTrash); err != nil {
		return fmt.Errorf("move to trash: %w", err)
	}

	return nil
}

// Restore restores the message from trash.
// Skips re-authorization since this message handle was already authorized.
func (m *message) Restore(ctx context.Context) error {
	if err := m.mailbox.checkAccess(); err != nil {
		return err
	}

	// Check current state - need to re-fetch to verify in trash
	current, err := m.mailbox.service.store.Get(ctx, m.GetID())
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if current.GetFolderID() != store.FolderTrash {
		return ErrNotInTrash
	}

	// Restore to inbox for received messages, sent for sent messages
	var folderID string
	if store.IsSentByOwner(current.GetOwnerID(), current.GetSenderID()) {
		folderID = store.FolderSent
	} else {
		folderID = store.FolderInbox
	}

	if err := m.mailbox.service.store.MoveToFolder(ctx, m.GetID(), folderID); err != nil {
		return fmt.Errorf("restore message: %w", err)
	}

	return nil
}

// PermanentlyDelete permanently deletes the message.
// Skips re-authorization since this message handle was already authorized.
func (m *message) PermanentlyDelete(ctx context.Context) error {
	if err := m.mailbox.checkAccess(); err != nil {
		return err
	}

	// Check current state - need to re-fetch to verify in trash
	current, err := m.mailbox.service.store.Get(ctx, m.GetID())
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if current.GetFolderID() != store.FolderTrash {
		return ErrNotInTrash
	}

	// Hard delete the message
	if err := m.mailbox.service.store.HardDelete(ctx, m.GetID()); err != nil {
		return fmt.Errorf("hard delete message: %w", err)
	}

	// Release attachment references
	if m.mailbox.service.attachments != nil {
		for _, a := range current.GetAttachments() {
			if err := m.mailbox.service.attachments.RemoveRef(ctx, a.GetID()); err != nil {
				m.mailbox.service.logger.Error("failed to release attachment ref during permanent delete",
					"error", err, "attachment_id", a.GetID())
			}
		}
	}

	return nil
}

// AddTag adds a tag to the message.
// Skips re-authorization since this message handle was already authorized.
func (m *message) AddTag(ctx context.Context, tagID string) error {
	if err := m.mailbox.checkAccess(); err != nil {
		return err
	}

	if tagID == "" {
		return fmt.Errorf("%w: empty tag ID", ErrInvalidID)
	}
	if len(tagID) > MaxTagIDLength {
		return fmt.Errorf("%w: tag ID exceeds maximum length of %d", ErrInvalidID, MaxTagIDLength)
	}

	if err := m.mailbox.service.store.AddTag(ctx, m.GetID(), tagID); err != nil {
		return fmt.Errorf("add tag: %w", err)
	}

	return nil
}

// RemoveTag removes a tag from the message.
// Skips re-authorization since this message handle was already authorized.
func (m *message) RemoveTag(ctx context.Context, tagID string) error {
	if err := m.mailbox.checkAccess(); err != nil {
		return err
	}

	if tagID == "" {
		return fmt.Errorf("%w: empty tag ID", ErrInvalidID)
	}
	if len(tagID) > MaxTagIDLength {
		return fmt.Errorf("%w: tag ID exceeds maximum length of %d", ErrInvalidID, MaxTagIDLength)
	}

	if err := m.mailbox.service.store.RemoveTag(ctx, m.GetID(), tagID); err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}

	return nil
}

// Compile-time check that message implements Message.
var _ Message = (*message)(nil)

// MessageListReader provides read-only access to a paginated list of messages.
type MessageListReader interface {
	// All returns all messages in this list.
	All() []Message
	// Total returns the total count of messages matching the query (not just this page).
	Total() int64
	// HasMore returns true if there are more messages after this page.
	HasMore() bool
	// NextCursor returns the cursor for fetching the next page.
	NextCursor() string
	// IDs returns the IDs of all messages in this list.
	IDs() []string
}

// MessageListMutator provides bulk mutation operations on a list of messages.
type MessageListMutator interface {
	// MarkRead marks all messages in this list as read.
	MarkRead(ctx context.Context) (*BulkResult, error)
	// MarkUnread marks all messages in this list as unread.
	MarkUnread(ctx context.Context) (*BulkResult, error)
	// Move moves all messages in this list to the specified folder.
	Move(ctx context.Context, folderID string) (*BulkResult, error)
	// Delete moves all messages in this list to trash.
	Delete(ctx context.Context) (*BulkResult, error)
	// Archive moves all messages in this list to the archive folder.
	Archive(ctx context.Context) (*BulkResult, error)
	// AddTag adds a tag to all messages in this list.
	AddTag(ctx context.Context, tagID string) (*BulkResult, error)
	// RemoveTag removes a tag from all messages in this list.
	RemoveTag(ctx context.Context, tagID string) (*BulkResult, error)
}

// MessageList provides access to a paginated list of messages with bulk operations.
//
// Composed of:
//   - MessageListReader: Read-only access (All, Total, HasMore, NextCursor, IDs)
//   - MessageListMutator: Bulk mutations (MarkRead, Move, Delete, Archive, AddTag, etc.)
type MessageList interface {
	MessageListReader
	MessageListMutator
}

// OperationResult contains the result of a single operation within a bulk operation.
// Results are returned in the same order as the input items.
type OperationResult struct {
	// ID is the identifier of the item that was processed.
	ID string
	// Success indicates whether the operation succeeded.
	Success bool
	// Error contains the error if the operation failed (nil if successful).
	Error error
	// Message contains the sent message (only for DraftList.Send, only if successful).
	Message Message
}

// BulkResult contains the result of a bulk operation.
// Used consistently across MessageList and DraftList operations.
//
// Results are returned in order, matching the input order.
// Use helper methods to check status and iterate results.
type BulkResult struct {
	// Results contains the outcome of each operation in input order.
	Results []OperationResult
}

// SuccessCount returns the number of successful operations.
func (r *BulkResult) SuccessCount() int {
	count := 0
	for _, res := range r.Results {
		if res.Success {
			count++
		}
	}
	return count
}

// FailureCount returns the number of failed operations.
func (r *BulkResult) FailureCount() int {
	count := 0
	for _, res := range r.Results {
		if !res.Success {
			count++
		}
	}
	return count
}

// HasFailures returns true if any operations failed.
func (r *BulkResult) HasFailures() bool {
	for _, res := range r.Results {
		if !res.Success {
			return true
		}
	}
	return false
}

// TotalCount returns the total number of items processed.
func (r *BulkResult) TotalCount() int {
	return len(r.Results)
}

// FailedIDs returns the IDs of items that failed.
func (r *BulkResult) FailedIDs() []string {
	var ids []string
	for _, res := range r.Results {
		if !res.Success {
			ids = append(ids, res.ID)
		}
	}
	return ids
}

// SuccessfulIDs returns the IDs of items that succeeded.
func (r *BulkResult) SuccessfulIDs() []string {
	var ids []string
	for _, res := range r.Results {
		if res.Success {
			ids = append(ids, res.ID)
		}
	}
	return ids
}

// SentMessages returns all successfully sent messages (for DraftList.Send operations).
func (r *BulkResult) SentMessages() []Message {
	var msgs []Message
	for _, res := range r.Results {
		if res.Success && res.Message != nil {
			msgs = append(msgs, res.Message)
		}
	}
	return msgs
}

// Err returns an error if there are failures, nil otherwise.
func (r *BulkResult) Err() error {
	if !r.HasFailures() {
		return nil
	}
	return &BulkOperationError{Result: r}
}

// BulkOperationError is returned when a bulk operation has partial failures.
// It wraps BulkResult to provide error interface while guaranteeing non-empty Error().
type BulkOperationError struct {
	Result *BulkResult
}

// Error implements the error interface.
// Always returns a non-empty string describing the failure.
func (e *BulkOperationError) Error() string {
	return fmt.Sprintf("mailbox: bulk operation failed for %d of %d items",
		e.Result.FailureCount(), e.Result.TotalCount())
}

// messageList is the internal implementation of MessageList.
type messageList struct {
	messages   []Message
	total      int64
	hasMore    bool
	nextCursor string
	mailbox    *userMailbox
}

// wrapMessageList converts a store.MessageList to a mailbox.MessageList.
func wrapMessageList(list *store.MessageList, m *userMailbox) MessageList {
	messages := make([]Message, len(list.Messages))
	for i, msg := range list.Messages {
		messages[i] = newMessage(msg, m)
	}
	return &messageList{
		messages:   messages,
		total:      list.Total,
		hasMore:    list.HasMore,
		nextCursor: list.NextCursor,
		mailbox:    m,
	}
}

// Data access methods

func (l *messageList) All() []Message       { return l.messages }
func (l *messageList) Total() int64         { return l.total }
func (l *messageList) HasMore() bool        { return l.hasMore }
func (l *messageList) NextCursor() string   { return l.nextCursor }

func (l *messageList) IDs() []string {
	ids := make([]string, len(l.messages))
	for i, msg := range l.messages {
		ids[i] = msg.GetID()
	}
	return ids
}

// Bulk operations

func (l *messageList) MarkRead(ctx context.Context) (*BulkResult, error) {
	return l.bulkUpdateFlags(ctx, MarkRead())
}

func (l *messageList) MarkUnread(ctx context.Context) (*BulkResult, error) {
	return l.bulkUpdateFlags(ctx, MarkUnread())
}

func (l *messageList) Archive(ctx context.Context) (*BulkResult, error) {
	return l.Move(ctx, store.FolderArchived)
}

func (l *messageList) Move(ctx context.Context, folderID string) (*BulkResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.messages))}

	for _, msg := range l.messages {
		res := OperationResult{ID: msg.GetID()}
		if err := msg.Move(ctx, folderID); err != nil {
			res.Error = err
		} else {
			res.Success = true
		}
		result.Results = append(result.Results, res)
	}

	return result, result.Err()
}

func (l *messageList) Delete(ctx context.Context) (*BulkResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.messages))}

	for _, msg := range l.messages {
		res := OperationResult{ID: msg.GetID()}
		if err := msg.Delete(ctx); err != nil {
			res.Error = err
		} else {
			res.Success = true
		}
		result.Results = append(result.Results, res)
	}

	return result, result.Err()
}

func (l *messageList) AddTag(ctx context.Context, tagID string) (*BulkResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.messages))}

	for _, msg := range l.messages {
		res := OperationResult{ID: msg.GetID()}
		if err := msg.AddTag(ctx, tagID); err != nil {
			res.Error = err
		} else {
			res.Success = true
		}
		result.Results = append(result.Results, res)
	}

	return result, result.Err()
}

func (l *messageList) RemoveTag(ctx context.Context, tagID string) (*BulkResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.messages))}

	for _, msg := range l.messages {
		res := OperationResult{ID: msg.GetID()}
		if err := msg.RemoveTag(ctx, tagID); err != nil {
			res.Error = err
		} else {
			res.Success = true
		}
		result.Results = append(result.Results, res)
	}

	return result, result.Err()
}

// bulkUpdateFlags applies flag updates to all messages in the list.
func (l *messageList) bulkUpdateFlags(ctx context.Context, flags Flags) (*BulkResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.messages))}

	for _, msg := range l.messages {
		res := OperationResult{ID: msg.GetID()}
		if err := msg.Update(ctx, flags); err != nil {
			res.Error = err
		} else {
			res.Success = true
		}
		result.Results = append(result.Results, res)
	}

	return result, result.Err()
}

// Compile-time check that messageList implements MessageList.
var _ MessageList = (*messageList)(nil)
