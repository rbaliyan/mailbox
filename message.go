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
// Delegates to userMailbox.UpdateFlags to ensure consistent event publishing.
func (m *message) Update(ctx context.Context, flags Flags) error {
	return m.mailbox.UpdateFlags(ctx, m.GetID(), flags)
}

// Move moves the message to a folder.
// Delegates to userMailbox.MoveToFolder for consistent behavior.
func (m *message) Move(ctx context.Context, folderID string) error {
	return m.mailbox.MoveToFolder(ctx, m.GetID(), folderID)
}

// Delete moves the message to trash.
// Delegates to userMailbox.Delete for consistent behavior.
func (m *message) Delete(ctx context.Context) error {
	return m.mailbox.Delete(ctx, m.GetID())
}

// Restore restores the message from trash.
// Delegates to userMailbox.Restore for consistent behavior.
func (m *message) Restore(ctx context.Context) error {
	return m.mailbox.Restore(ctx, m.GetID())
}

// PermanentlyDelete permanently deletes the message.
// Delegates to userMailbox.PermanentlyDelete for consistent event publishing.
func (m *message) PermanentlyDelete(ctx context.Context) error {
	return m.mailbox.PermanentlyDelete(ctx, m.GetID())
}

// AddTag adds a tag to the message.
// Delegates to userMailbox.AddTag for consistent behavior.
func (m *message) AddTag(ctx context.Context, tagID string) error {
	return m.mailbox.AddTag(ctx, m.GetID(), tagID)
}

// RemoveTag removes a tag from the message.
// Delegates to userMailbox.RemoveTag for consistent behavior.
func (m *message) RemoveTag(ctx context.Context, tagID string) error {
	return m.mailbox.RemoveTag(ctx, m.GetID(), tagID)
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
