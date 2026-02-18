package mailbox

import (
	"context"
	"fmt"

	"github.com/rbaliyan/mailbox/store"
)

// DraftReader provides read access to draft content.
type DraftReader interface {
	ID() string
	Subject() string
	Body() string
	RecipientIDs() []string
	Headers() map[string]string
	Metadata() map[string]any
	Attachments() []store.Attachment
	ThreadID() string
	ReplyToID() string
}

// DraftComposer provides fluent setter methods for composing a draft.
// All setter methods return DraftComposer to enable chaining:
//
//	draft.SetRecipients("user1").SetSubject("Hello").SetBody("World")
//
// For operations that can fail (AddAttachment, ReplyTo), use the methods
// on Draft directly — they are not part of the fluent interface.
type DraftComposer interface {
	SetRecipients(recipientIDs ...string) DraftComposer
	SetSubject(subject string) DraftComposer
	SetBody(body string) DraftComposer
	SetHeader(key, value string) DraftComposer
	SetMetadata(key string, value any) DraftComposer
}

// DraftPreparer provides draft preparation methods that can fail.
// These methods return errors and are intentionally not part of the fluent
// DraftComposer interface to keep the builder pattern clean.
type DraftPreparer interface {
	// AddAttachment adds an attachment after validating it.
	// Returns an error if the attachment is invalid or limits are exceeded.
	AddAttachment(attachment store.Attachment) error

	// ReplyTo sets this draft as a reply to another message.
	// It looks up the parent message to inherit the thread ID.
	// Returns an error if the parent message cannot be found or accessed.
	ReplyTo(ctx context.Context, messageID string) error

	// Forward prepares this draft as a forward of another message.
	// Copies subject (with "Fwd:" prefix), body, and attachments from the original.
	// Returns an error if the original message cannot be found or accessed.
	Forward(ctx context.Context, messageID string) error
}

// DraftPublisher provides lifecycle operations that transform or persist a draft.
//
// Validation differences between Save() and Send():
//   - Send() requires: non-empty recipients, non-empty subject, valid content
//   - Save() allows: empty recipients, empty subject (for work-in-progress drafts)
//   - Both validate: body size limits, metadata limits, attachment limits
//
// This allows users to save incomplete drafts and complete them later.
type DraftPublisher interface {
	// Send validates fully and sends the draft.
	// Requires: at least one recipient and a non-empty subject.
	// Creates recipient copies and moves sender's copy to sent folder.
	// Returns ErrEmptyRecipients if no recipients, ErrEmptySubject if no subject.
	Send(ctx context.Context) (Message, error)

	// Save saves the draft without sending.
	// Allows incomplete drafts (empty recipients, empty subject) for later editing.
	// Validates: body size, metadata size, attachment limits.
	// The draft can be retrieved later via Drafts() and completed.
	Save(ctx context.Context) (Draft, error)
}

// DraftMutator provides mutation operations on a draft.
type DraftMutator interface {
	// Delete deletes the draft.
	// If the draft was saved, it's permanently deleted from storage.
	// If the draft was not saved, this is a no-op.
	Delete(ctx context.Context) error
}

// Draft represents a message being composed.
// Use Mailbox.Compose() to create a new draft.
//
// Composed of:
//   - DraftReader: Read draft content (ID, Subject, Body, etc.)
//   - DraftComposer: Fluent setters (SetSubject, SetBody, SetRecipients, SetMetadata)
//   - DraftPreparer: Failable operations (AddAttachment, ReplyTo)
//   - DraftPublisher: Lifecycle operations (Send, Save)
//   - DraftMutator: Mutation operations (Delete)
//
// Usage pattern:
//
//	draft, _ := mailbox.Compose()
//	draft.SetRecipients("user1").SetSubject("Hello").SetBody("World")  // fluent chain
//	if err := draft.AddAttachment(att); err != nil { ... }             // separate call
//	if err := draft.ReplyTo(ctx, parentID); err != nil { ... }         // separate call
//	msg, err := draft.Send(ctx)
type Draft interface {
	DraftReader
	DraftComposer
	DraftPreparer
	DraftPublisher
	DraftMutator
}

// draft is the internal implementation of Draft.
type draft struct {
	mailbox   *userMailbox
	message   store.DraftMessage
	saved     bool
	threadID  string
	replyToID string
}

// newDraft creates a new draft for the given mailbox.
func newDraft(m *userMailbox) *draft {
	msg := m.service.store.NewDraft(m.userID)

	return &draft{
		mailbox: m,
		message: msg,
		saved:   false,
	}
}

// ID returns the draft ID if saved, empty string otherwise.
func (d *draft) ID() string {
	return d.message.GetID()
}

// Subject returns the draft subject.
func (d *draft) Subject() string {
	return d.message.GetSubject()
}

// Body returns the draft body.
func (d *draft) Body() string {
	return d.message.GetBody()
}

// RecipientIDs returns the recipient IDs.
func (d *draft) RecipientIDs() []string {
	return d.message.GetRecipientIDs()
}

// Headers returns the draft headers.
func (d *draft) Headers() map[string]string {
	return d.message.GetHeaders()
}

// Metadata returns the draft metadata.
func (d *draft) Metadata() map[string]any {
	return d.message.GetMetadata()
}

// Attachments returns the draft attachments.
func (d *draft) Attachments() []store.Attachment {
	return d.message.GetAttachments()
}

// SetRecipients sets the recipient IDs.
func (d *draft) SetRecipients(recipientIDs ...string) DraftComposer {
	d.message.SetRecipients(recipientIDs...)
	return d
}

// SetSubject sets the subject.
func (d *draft) SetSubject(subject string) DraftComposer {
	d.message.SetSubject(subject)
	return d
}

// SetBody sets the body.
func (d *draft) SetBody(body string) DraftComposer {
	d.message.SetBody(body)
	return d
}

// SetHeader sets a header key-value pair.
func (d *draft) SetHeader(key, value string) DraftComposer {
	d.message.SetHeader(key, value)
	return d
}

// SetMetadata sets a metadata key-value pair.
func (d *draft) SetMetadata(key string, value any) DraftComposer {
	d.message.SetMetadata(key, value)
	return d
}

// AddAttachment adds an attachment after validating it.
// Returns an error if the attachment is invalid or limits are exceeded.
func (d *draft) AddAttachment(attachment store.Attachment) error {
	if attachment == nil {
		return ErrInvalidAttachment
	}

	limits := d.mailbox.service.opts.getLimits()

	if attachment.GetFilename() == "" {
		return &ValidationError{Field: "attachment.filename", Message: "filename is required"}
	}
	if attachment.GetContentType() == "" {
		return &ValidationError{Field: "attachment.content_type", Message: "content type is required"}
	}
	if attachment.GetSize() > limits.MaxAttachmentSize {
		return &ValidationError{
			Field:   "attachment.size",
			Message: fmt.Sprintf("attachment size %d exceeds limit %d", attachment.GetSize(), limits.MaxAttachmentSize),
		}
	}

	if len(d.message.GetAttachments()) >= limits.MaxAttachmentCount {
		return &ValidationError{
			Field:   "attachments",
			Message: fmt.Sprintf("attachment count would exceed limit %d", limits.MaxAttachmentCount),
		}
	}

	d.message.AddAttachment(attachment)
	return nil
}

// ReplyTo sets this draft as a reply to another message.
// It looks up the parent message to inherit the thread ID.
// Returns an error if the parent message cannot be found or accessed.
func (d *draft) ReplyTo(ctx context.Context, messageID string) error {
	if messageID == "" {
		return &ValidationError{Field: "message_id", Message: "message ID is required for reply"}
	}

	parent, err := d.mailbox.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get parent message: %w", err)
	}

	// Verify the user can access the parent message
	if !d.mailbox.canAccess(parent) {
		return ErrUnauthorized
	}

	d.replyToID = messageID
	if tid := parent.GetThreadID(); tid != "" {
		d.threadID = tid
	} else {
		// First reply in a conversation: use the parent's message ID as thread ID.
		d.threadID = messageID
	}

	return nil
}

// Forward prepares this draft as a forward of another message.
// Copies subject (with "Fwd:" prefix), body, and attachments from the original.
func (d *draft) Forward(ctx context.Context, messageID string) error {
	if messageID == "" {
		return &ValidationError{Field: "message_id", Message: "message ID is required for forward"}
	}

	original, err := d.mailbox.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get original message: %w", err)
	}

	if !d.mailbox.canAccess(original) {
		return ErrUnauthorized
	}

	// Copy subject with "Fwd:" prefix (avoid double-prefixing)
	subject := original.GetSubject()
	if len(subject) < 4 || subject[:4] != "Fwd:" {
		subject = "Fwd: " + subject
	}
	d.message.SetSubject(subject)
	d.message.SetBody(original.GetBody())

	// Copy attachments
	for _, a := range original.GetAttachments() {
		d.message.AddAttachment(a)
	}

	return nil
}

// ThreadID returns the thread ID if this is part of a thread.
func (d *draft) ThreadID() string {
	return d.threadID
}

// ReplyToID returns the message ID this is a reply to.
func (d *draft) ReplyToID() string {
	return d.replyToID
}

// Send validates and sends the draft.
// Creates recipient copies and moves sender's copy to sent folder.
// On partial delivery, returns both the sent message and a PartialDeliveryError.
// On event publish failure, returns both the sent message and an EventPublishError.
func (d *draft) Send(ctx context.Context) (Message, error) {
	msg, err := d.mailbox.sendDraft(ctx, d.message, d.threadID, d.replyToID)
	if msg != nil {
		d.saved = true
		return newMessage(msg, d.mailbox), err
	}
	return nil, err
}

// Save saves the draft without sending.
// The draft can be retrieved later and sent.
func (d *draft) Save(ctx context.Context) (Draft, error) {
	savedMsg, err := d.mailbox.saveDraft(ctx, d.message)
	if err != nil {
		return nil, err
	}
	d.message = savedMsg
	d.saved = true
	return d, nil
}

// Delete deletes the draft.
// If the draft was saved, it's permanently deleted from storage.
// If the draft was not saved, this is a no-op.
func (d *draft) Delete(ctx context.Context) error {
	if !d.saved || d.message.GetID() == "" {
		// Draft was never saved, nothing to delete
		return nil
	}

	// Permanently delete the draft
	return d.mailbox.service.store.DeleteDraft(ctx, d.message.GetID())
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

	for i, draft := range l.drafts {
		if err := ctx.Err(); err != nil {
			// Batch-append all remaining items as cancelled and break
			for _, remaining := range l.drafts[i:] {
				if remaining.ID() != "" {
					result.Results = append(result.Results, OperationResult{ID: remaining.ID(), Error: err})
				}
			}
			break
		}
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
func (l *draftList) Send(ctx context.Context) (*DraftSendResult, error) {
	result := &BulkResult{Results: make([]OperationResult, 0, len(l.drafts))}
	var sentMessages []Message

	for i, draft := range l.drafts {
		if err := ctx.Err(); err != nil {
			// Batch-append all remaining items as cancelled and break
			for _, remaining := range l.drafts[i:] {
				draftID := remaining.ID()
				if draftID == "" {
					draftID = "unsaved-draft"
				}
				result.Results = append(result.Results, OperationResult{ID: draftID, Error: err})
			}
			break
		}
		draftID := draft.ID()
		if draftID == "" {
			draftID = "unsaved-draft"
		}
		res := OperationResult{ID: draftID}
		msg, err := draft.Send(ctx)
		if msg != nil {
			// Message was created (even on partial delivery or event errors)
			sentMessages = append(sentMessages, msg)
			res.Success = true
		}
		if err != nil {
			res.Error = err
			if msg == nil {
				res.Success = false
			}
		}
		result.Results = append(result.Results, res)
	}

	return &DraftSendResult{BulkResult: result, sentMessages: sentMessages}, result.Err()
}
