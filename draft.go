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
	Metadata() map[string]any
	Attachments() []store.Attachment
	ThreadID() string
	ReplyToID() string
}

// DraftComposer provides methods for composing a draft.
type DraftComposer interface {
	SetRecipients(recipientIDs ...string) Draft
	SetSubject(subject string) Draft
	SetBody(body string) Draft
	SetMetadata(key string, value any) Draft
	AddAttachment(attachment store.Attachment) error
	ReplyTo(ctx context.Context, messageID string) error
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
//   - DraftComposer: Set draft content (SetSubject, SetBody, AddAttachment, etc.)
//   - DraftPublisher: Lifecycle operations (Send, Save)
//   - DraftMutator: Mutation operations (Delete)
type Draft interface {
	DraftReader
	DraftComposer
	DraftPublisher
	DraftMutator
}

// draft is the internal implementation of Draft.
type draft struct {
	mailbox  *userMailbox
	message  store.DraftMessage
	saved    bool
	threadID string
	replyToID    string
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

// Metadata returns the draft metadata.
func (d *draft) Metadata() map[string]any {
	return d.message.GetMetadata()
}

// Attachments returns the draft attachments.
func (d *draft) Attachments() []store.Attachment {
	return d.message.GetAttachments()
}

// SetRecipients sets the recipient IDs.
func (d *draft) SetRecipients(recipientIDs ...string) Draft {
	d.message.SetRecipients(recipientIDs...)
	return d
}

// SetSubject sets the subject.
func (d *draft) SetSubject(subject string) Draft {
	d.message.SetSubject(subject)
	return d
}

// SetBody sets the body.
func (d *draft) SetBody(body string) Draft {
	d.message.SetBody(body)
	return d
}

// SetMetadata sets a metadata key-value pair.
func (d *draft) SetMetadata(key string, value any) Draft {
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
func (d *draft) Send(ctx context.Context) (Message, error) {
	msg, err := d.mailbox.sendDraft(ctx, d.message, d.threadID, d.replyToID)
	if err != nil {
		return nil, err
	}
	// Mark as saved since it's now in the store
	d.saved = true
	return newMessage(msg, d.mailbox), nil
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
