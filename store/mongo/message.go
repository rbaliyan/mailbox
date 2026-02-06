package mongo

import (
	"time"

	"github.com/rbaliyan/mailbox/store"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Compile-time checks
var _ store.Message = (*message)(nil)
var _ store.DraftMessage = (*message)(nil)
var _ store.Attachment = (*attachment)(nil)

// messageDoc is the MongoDB document representation.
type messageDoc struct {
	ID             bson.ObjectID   `bson:"_id,omitempty"`
	OwnerID        string          `bson:"owner_id"`
	SenderID       string          `bson:"sender_id"`
	RecipientIDs   []string        `bson:"recipient_ids"`
	Subject        string          `bson:"subject"`
	Body           string          `bson:"body"`
	Metadata       map[string]any  `bson:"metadata,omitempty"`
	Status         string          `bson:"status"`
	IsRead         bool            `bson:"is_read"`
	ReadAt         *time.Time      `bson:"read_at,omitempty"`
	FolderID       string          `bson:"folder_id"`
	Tags           []string        `bson:"tags,omitempty"`
	Attachments    []attachmentDoc `bson:"attachments,omitempty"`
	CreatedAt      time.Time       `bson:"created_at"`
	UpdatedAt      time.Time       `bson:"updated_at"`
	IsDraft        bool            `bson:"__is_draft,omitempty"`
	IdempotencyKey string          `bson:"idempotency_key,omitempty"` // For atomic idempotent creates
	ThreadID       string          `bson:"thread_id,omitempty"`
	ReplyToID      string          `bson:"reply_to_id,omitempty"`
}

// attachmentDoc is the MongoDB document for attachments.
type attachmentDoc struct {
	ID          string    `bson:"id"`
	Filename    string    `bson:"filename"`
	ContentType string    `bson:"content_type"`
	Size        int64     `bson:"size"`
	URI         string    `bson:"uri"`
	CreatedAt   time.Time `bson:"created_at"`
}

// message implements both store.Message and store.DraftMessage for MongoDB.
type message struct {
	id           string
	ownerID      string
	senderID     string
	recipientIDs []string
	subject      string
	body         string
	metadata     map[string]any
	status       store.MessageStatus
	isRead       bool
	readAt       *time.Time
	folderID     string
	tags         []string
	attachments  []*attachment
	createdAt    time.Time
	updatedAt    time.Time
	isDraft      bool
	threadID     string
	replyToID    string

	// delta tracking (internal use only)
	delta messageDelta
}

// messageDelta tracks changes for efficient updates.
type messageDelta struct {
	subject       *string
	body          *string
	recipientIDs  []string
	recipientsSet bool
	metadata      map[string]any
}

// attachment implements store.Attachment for MongoDB.
type attachment struct {
	id          string
	filename    string
	contentType string
	size        int64
	uri         string
	createdAt   time.Time
}

// =============================================================================
// Message getters (implements store.Message)
// =============================================================================

func (m *message) GetID() string                  { return m.id }
func (m *message) GetOwnerID() string             { return m.ownerID }
func (m *message) GetSenderID() string            { return m.senderID }
func (m *message) GetRecipientIDs() []string      { return m.recipientIDs }
func (m *message) GetSubject() string             { return m.subject }
func (m *message) GetBody() string                { return m.body }
func (m *message) GetMetadata() map[string]any    { return m.metadata }
func (m *message) GetStatus() store.MessageStatus { return m.status }
func (m *message) GetIsRead() bool                { return m.isRead }
func (m *message) GetReadAt() *time.Time          { return m.readAt }
func (m *message) GetFolderID() string            { return m.folderID }
func (m *message) GetTags() []string              { return m.tags }
func (m *message) GetCreatedAt() time.Time        { return m.createdAt }
func (m *message) GetUpdatedAt() time.Time        { return m.updatedAt }
func (m *message) GetThreadID() string            { return m.threadID }
func (m *message) GetReplyToID() string           { return m.replyToID }
func (m *message) GetAttachments() []store.Attachment {
	if m.attachments == nil {
		return nil
	}
	result := make([]store.Attachment, len(m.attachments))
	for i, a := range m.attachments {
		result[i] = a
	}
	return result
}

// =============================================================================
// Draft setters (implements store.DraftMessage fluent API)
// =============================================================================

func (m *message) SetSubject(subject string) store.DraftMessage {
	m.subject = subject
	m.delta.subject = &subject
	return m
}

func (m *message) SetBody(body string) store.DraftMessage {
	m.body = body
	m.delta.body = &body
	return m
}

func (m *message) SetRecipients(recipientIDs ...string) store.DraftMessage {
	m.recipientIDs = recipientIDs
	m.delta.recipientIDs = recipientIDs
	m.delta.recipientsSet = true
	return m
}

func (m *message) SetMetadata(key string, value any) store.DraftMessage {
	if m.metadata == nil {
		m.metadata = make(map[string]any)
	}
	if m.delta.metadata == nil {
		m.delta.metadata = make(map[string]any)
	}
	m.metadata[key] = value
	m.delta.metadata[key] = value
	return m
}

func (m *message) AddAttachment(att store.Attachment) store.DraftMessage {
	if att == nil {
		return m
	}
	a := &attachment{
		id:          att.GetID(),
		filename:    att.GetFilename(),
		contentType: att.GetContentType(),
		size:        att.GetSize(),
		uri:         att.GetURI(),
		createdAt:   att.GetCreatedAt(),
	}
	m.attachments = append(m.attachments, a)
	return m
}

// =============================================================================
// Internal delta tracking methods
// =============================================================================

func (m *message) hasChanges() bool {
	return m.delta.subject != nil ||
		m.delta.body != nil ||
		m.delta.recipientsSet ||
		len(m.delta.metadata) > 0
}

func (m *message) resetDelta() {
	m.delta = messageDelta{}
}

// =============================================================================
// Attachment getters
// =============================================================================

func (a *attachment) GetID() string           { return a.id }
func (a *attachment) GetFilename() string     { return a.filename }
func (a *attachment) GetContentType() string  { return a.contentType }
func (a *attachment) GetSize() int64          { return a.size }
func (a *attachment) GetURI() string          { return a.uri }
func (a *attachment) GetCreatedAt() time.Time { return a.createdAt }

// =============================================================================
// Conversion functions
// =============================================================================

func messageToDoc(msg *message) *messageDoc {
	doc := &messageDoc{
		OwnerID:      msg.ownerID,
		SenderID:     msg.senderID,
		RecipientIDs: msg.recipientIDs,
		Subject:      msg.subject,
		Body:         msg.body,
		Metadata:     msg.metadata,
		Status:       string(msg.status),
		IsRead:       msg.isRead,
		ReadAt:       msg.readAt,
		FolderID:     msg.folderID,
		Tags:         msg.tags,
		CreatedAt:    msg.createdAt,
		UpdatedAt:    msg.updatedAt,
		IsDraft:      msg.isDraft,
		ThreadID:     msg.threadID,
		ReplyToID:    msg.replyToID,
	}

	if len(msg.attachments) > 0 {
		doc.Attachments = make([]attachmentDoc, len(msg.attachments))
		for i, a := range msg.attachments {
			doc.Attachments[i] = attachmentDoc{
				ID:          a.id,
				Filename:    a.filename,
				ContentType: a.contentType,
				Size:        a.size,
				URI:         a.uri,
				CreatedAt:   a.createdAt,
			}
		}
	}

	if msg.id != "" {
		if oid, err := bson.ObjectIDFromHex(msg.id); err == nil {
			doc.ID = oid
		}
	}
	return doc
}

func docToMessage(doc *messageDoc) *message {
	msg := &message{
		id:           doc.ID.Hex(),
		ownerID:      doc.OwnerID,
		senderID:     doc.SenderID,
		recipientIDs: doc.RecipientIDs,
		subject:      doc.Subject,
		body:         doc.Body,
		metadata:     doc.Metadata,
		status:       store.MessageStatus(doc.Status),
		isRead:       doc.IsRead,
		readAt:       doc.ReadAt,
		folderID:     doc.FolderID,
		tags:         doc.Tags,
		createdAt:    doc.CreatedAt,
		updatedAt:    doc.UpdatedAt,
		isDraft:      doc.IsDraft,
		threadID:     doc.ThreadID,
		replyToID:    doc.ReplyToID,
	}

	if len(doc.Attachments) > 0 {
		msg.attachments = make([]*attachment, len(doc.Attachments))
		for i, a := range doc.Attachments {
			msg.attachments[i] = &attachment{
				id:          a.ID,
				filename:    a.Filename,
				contentType: a.ContentType,
				size:        a.Size,
				uri:         a.URI,
				createdAt:   a.CreatedAt,
			}
		}
	}

	return msg
}
