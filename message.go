package mailbox

import (
	"context"

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
	return l.forEachMessage(ctx, func(msg Message) error { return msg.Move(ctx, folderID) })
}

func (l *messageList) Delete(ctx context.Context) (*BulkResult, error) {
	return l.forEachMessage(ctx, func(msg Message) error { return msg.Delete(ctx) })
}

func (l *messageList) AddTag(ctx context.Context, tagID string) (*BulkResult, error) {
	return l.forEachMessage(ctx, func(msg Message) error { return msg.AddTag(ctx, tagID) })
}

func (l *messageList) RemoveTag(ctx context.Context, tagID string) (*BulkResult, error) {
	return l.forEachMessage(ctx, func(msg Message) error { return msg.RemoveTag(ctx, tagID) })
}

// bulkUpdateFlags applies flag updates to all messages in the list.
func (l *messageList) bulkUpdateFlags(ctx context.Context, flags Flags) (*BulkResult, error) {
	return l.forEachMessage(ctx, func(msg Message) error { return msg.Update(ctx, flags) })
}

// forEachMessage applies an operation to each message, collecting results.
// Checks for context cancellation between iterations to support early termination.
func (l *messageList) forEachMessage(ctx context.Context, op func(Message) error) (*BulkResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.messages))}
	for i, msg := range l.messages {
		if err := ctx.Err(); err != nil {
			// Batch-append all remaining items as cancelled and break
			for _, remaining := range l.messages[i:] {
				result.Results = append(result.Results, OperationResult{ID: remaining.GetID(), Error: err})
			}
			break
		}
		res := OperationResult{ID: msg.GetID()}
		if err := op(msg); err != nil {
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

// Pre-allocated boolean pointers for efficient Flags creation.
// These avoid allocations when using MarkRead(), MarkUnread(), etc.
var (
	ptrTrue  = ptr(true)
	ptrFalse = ptr(false)
)

func ptr(b bool) *bool { return &b }

// Flags represents message flags that can be updated atomically.
// Use nil values to indicate no change.
type Flags struct {
	Read     *bool // nil = no change, true = mark read, false = mark unread
	Archived *bool // nil = no change, true = archive, false = unarchive
}

// Pre-allocated flag values for common operations.
// These are more efficient than calling MarkRead(), etc. in hot paths.
var (
	// FlagsMarkRead marks a message as read.
	FlagsMarkRead = Flags{Read: ptrTrue}
	// FlagsMarkUnread marks a message as unread.
	FlagsMarkUnread = Flags{Read: ptrFalse}
	// FlagsMarkArchived archives a message.
	FlagsMarkArchived = Flags{Archived: ptrTrue}
	// FlagsMarkUnarchived unarchives a message.
	FlagsMarkUnarchived = Flags{Archived: ptrFalse}
)

// NewFlags creates empty flags (no changes).
func NewFlags() Flags {
	return Flags{}
}

// WithRead returns flags with read status set.
func (f Flags) WithRead(read bool) Flags {
	if read {
		f.Read = ptrTrue
	} else {
		f.Read = ptrFalse
	}
	return f
}

// WithArchived returns flags with archived status set.
func (f Flags) WithArchived(archived bool) Flags {
	if archived {
		f.Archived = ptrTrue
	} else {
		f.Archived = ptrFalse
	}
	return f
}

// MarkRead returns flags to mark a message as read.
func MarkRead() Flags {
	return FlagsMarkRead
}

// MarkUnread returns flags to mark a message as unread.
func MarkUnread() Flags {
	return FlagsMarkUnread
}

// MarkArchived returns flags to archive a message.
func MarkArchived() Flags {
	return FlagsMarkArchived
}

// MarkUnarchived returns flags to unarchive a message.
func MarkUnarchived() Flags {
	return FlagsMarkUnarchived
}
