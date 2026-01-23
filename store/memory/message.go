package memory

import (
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// message is the internal representation for both drafts and messages.
type message struct {
	id               string
	ownerID          string
	senderID         string
	recipientIDs     []string
	subject          string
	body             string
	metadata         map[string]any
	status           store.MessageStatus
	isRead           bool
	readAt           *time.Time
	folderID         string
	tags             []string
	attachments      []store.Attachment
	createdAt        time.Time
	updatedAt        time.Time
	deleted          bool
	isDraft          bool // true for drafts, false for sent messages
	threadID         string
	replyToID        string
	reactions        []store.Reaction
	deliveryReceipts []store.DeliveryReceipt
}

// clone creates a deep copy of the message.
func (m *message) clone() *message {
	c := &message{
		id:        m.id,
		ownerID:   m.ownerID,
		senderID:  m.senderID,
		subject:   m.subject,
		body:      m.body,
		status:    m.status,
		isRead:    m.isRead,
		folderID:  m.folderID,
		createdAt: m.createdAt,
		updatedAt: m.updatedAt,
		deleted:   m.deleted,
		isDraft:   m.isDraft,
		threadID:  m.threadID,
		replyToID: m.replyToID,
	}

	if m.recipientIDs != nil {
		c.recipientIDs = make([]string, len(m.recipientIDs))
		copy(c.recipientIDs, m.recipientIDs)
	}
	if m.tags != nil {
		c.tags = make([]string, len(m.tags))
		copy(c.tags, m.tags)
	}
	if m.attachments != nil {
		c.attachments = make([]store.Attachment, len(m.attachments))
		copy(c.attachments, m.attachments)
	}
	if m.metadata != nil {
		c.metadata = make(map[string]any, len(m.metadata))
		for k, v := range m.metadata {
			c.metadata[k] = v
		}
	}
	if m.readAt != nil {
		t := *m.readAt
		c.readAt = &t
	}
	if m.reactions != nil {
		c.reactions = make([]store.Reaction, len(m.reactions))
		copy(c.reactions, m.reactions)
	}
	if m.deliveryReceipts != nil {
		c.deliveryReceipts = make([]store.DeliveryReceipt, len(m.deliveryReceipts))
		copy(c.deliveryReceipts, m.deliveryReceipts)
	}

	return c
}

// Message getters (implements store.Message)
func (m *message) GetID() string                             { return m.id }
func (m *message) GetOwnerID() string                        { return m.ownerID }
func (m *message) GetSenderID() string                       { return m.senderID }
func (m *message) GetRecipientIDs() []string                 { return m.recipientIDs }
func (m *message) GetSubject() string                        { return m.subject }
func (m *message) GetBody() string                           { return m.body }
func (m *message) GetMetadata() map[string]any               { return m.metadata }
func (m *message) GetStatus() store.MessageStatus            { return m.status }
func (m *message) GetIsRead() bool                           { return m.isRead }
func (m *message) GetReadAt() *time.Time                     { return m.readAt }
func (m *message) GetFolderID() string                       { return m.folderID }
func (m *message) GetTags() []string                         { return m.tags }
func (m *message) GetAttachments() []store.Attachment        { return m.attachments }
func (m *message) GetCreatedAt() time.Time                   { return m.createdAt }
func (m *message) GetUpdatedAt() time.Time                   { return m.updatedAt }
func (m *message) GetThreadID() string                       { return m.threadID }
func (m *message) GetReplyToID() string                      { return m.replyToID }
func (m *message) GetReactions() []store.Reaction            { return m.reactions }
func (m *message) GetDeliveryReceipts() []store.DeliveryReceipt { return m.deliveryReceipts }

// Draft setters (implements store.DraftMessage fluent API)
func (m *message) SetSubject(subject string) store.DraftMessage {
	m.subject = subject
	m.updatedAt = time.Now().UTC()
	return m
}

func (m *message) SetBody(body string) store.DraftMessage {
	m.body = body
	m.updatedAt = time.Now().UTC()
	return m
}

func (m *message) SetRecipients(recipientIDs ...string) store.DraftMessage {
	m.recipientIDs = recipientIDs
	m.updatedAt = time.Now().UTC()
	return m
}

func (m *message) SetMetadata(key string, value any) store.DraftMessage {
	if m.metadata == nil {
		m.metadata = make(map[string]any)
	}
	m.metadata[key] = value
	m.updatedAt = time.Now().UTC()
	return m
}

func (m *message) AddAttachment(attachment store.Attachment) store.DraftMessage {
	m.attachments = append(m.attachments, attachment)
	m.updatedAt = time.Now().UTC()
	return m
}

// Compile-time checks
var _ store.Message = (*message)(nil)
var _ store.DraftMessage = (*message)(nil)
