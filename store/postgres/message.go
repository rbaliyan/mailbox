package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/rbaliyan/mailbox/store"
)

// Compile-time checks
var _ store.Message = (*message)(nil)
var _ store.DraftMessage = (*message)(nil)
var _ store.Attachment = (*attachment)(nil)

// =============================================================================
// Message type
// =============================================================================

type message struct {
	id             string
	ownerID        string
	senderID       string
	recipientIDs   []string
	subject        string
	body           string
	headers        map[string]string
	metadata       map[string]any
	status         store.MessageStatus
	isRead         bool
	readAt         *time.Time
	folderID       string
	tags           []string
	attachments    []store.Attachment
	isDeleted      bool
	isDraft        bool
	idempotencyKey string
	threadID       string
	replyToID      string
	createdAt      time.Time
	updatedAt      time.Time
}

// Message getters
func (m *message) GetID() string                      { return m.id }
func (m *message) GetOwnerID() string                 { return m.ownerID }
func (m *message) GetSenderID() string                { return m.senderID }
func (m *message) GetRecipientIDs() []string          { return m.recipientIDs }
func (m *message) GetSubject() string                 { return m.subject }
func (m *message) GetBody() string                    { return m.body }
func (m *message) GetHeaders() map[string]string      { return m.headers }
func (m *message) GetMetadata() map[string]any        { return m.metadata }
func (m *message) GetStatus() store.MessageStatus     { return m.status }
func (m *message) GetIsRead() bool                    { return m.isRead }
func (m *message) GetReadAt() *time.Time              { return m.readAt }
func (m *message) GetFolderID() string                { return m.folderID }
func (m *message) GetTags() []string                  { return m.tags }
func (m *message) GetAttachments() []store.Attachment { return m.attachments }
func (m *message) GetCreatedAt() time.Time            { return m.createdAt }
func (m *message) GetUpdatedAt() time.Time            { return m.updatedAt }
func (m *message) GetThreadID() string                { return m.threadID }
func (m *message) GetReplyToID() string               { return m.replyToID }

// Draft setters (fluent)
func (m *message) SetSubject(subject string) store.DraftMessage {
	m.subject = subject
	return m
}

func (m *message) SetBody(body string) store.DraftMessage {
	m.body = body
	return m
}

func (m *message) SetRecipients(recipientIDs ...string) store.DraftMessage {
	m.recipientIDs = recipientIDs
	return m
}

func (m *message) SetHeader(key, value string) store.DraftMessage {
	if m.headers == nil {
		m.headers = make(map[string]string)
	}
	m.headers[key] = value
	return m
}

func (m *message) SetMetadata(key string, value any) store.DraftMessage {
	if m.metadata == nil {
		m.metadata = make(map[string]any)
	}
	m.metadata[key] = value
	return m
}

func (m *message) AddAttachment(attachment store.Attachment) store.DraftMessage {
	m.attachments = append(m.attachments, attachment)
	return m
}

// =============================================================================
// Attachment type
// =============================================================================

type attachmentDoc struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	URI         string    `json:"uri"`
	CreatedAt   time.Time `json:"created_at"`
}

type attachment struct {
	id          string
	filename    string
	contentType string
	size        int64
	uri         string
	createdAt   time.Time
}

func (a *attachment) GetID() string           { return a.id }
func (a *attachment) GetFilename() string     { return a.filename }
func (a *attachment) GetContentType() string  { return a.contentType }
func (a *attachment) GetSize() int64          { return a.size }
func (a *attachment) GetURI() string          { return a.uri }
func (a *attachment) GetCreatedAt() time.Time { return a.createdAt }

// =============================================================================
// Scanning and marshaling helpers
// =============================================================================

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanMessage(row rowScanner) (*message, error) {
	var msg message
	var headersJSON, metadataJSON, attachmentsJSON []byte
	var readAt sql.NullTime
	var idempotencyKey, threadID, replyToID sql.NullString

	err := row.Scan(
		&msg.id, &msg.ownerID, &msg.senderID, &msg.subject, &msg.body,
		&headersJSON, &metadataJSON, &msg.status, &msg.folderID, &msg.isRead, &readAt,
		pq.Array(&msg.recipientIDs), pq.Array(&msg.tags), &attachmentsJSON,
		&msg.isDeleted, &msg.isDraft, &idempotencyKey, &threadID, &replyToID,
		&msg.createdAt, &msg.updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if readAt.Valid {
		msg.readAt = &readAt.Time
	}
	if idempotencyKey.Valid {
		msg.idempotencyKey = idempotencyKey.String
	}
	if threadID.Valid {
		msg.threadID = threadID.String
	}
	if replyToID.Valid {
		msg.replyToID = replyToID.String
	}

	// Unmarshal JSON fields
	if len(headersJSON) > 0 {
		if err := json.Unmarshal(headersJSON, &msg.headers); err != nil {
			return nil, fmt.Errorf("unmarshal headers: %w", err)
		}
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &msg.metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}

	if len(attachmentsJSON) > 0 {
		msg.attachments, err = s.unmarshalAttachments(attachmentsJSON)
		if err != nil {
			return nil, fmt.Errorf("unmarshal attachments: %w", err)
		}
	}

	return &msg, nil
}

func (s *Store) scanMessageFromRows(rows *sql.Rows) (*message, error) {
	return s.scanMessage(rows)
}

func (s *Store) marshalAttachments(attachments []store.Attachment) ([]byte, error) {
	if len(attachments) == 0 {
		return []byte("[]"), nil
	}

	docs := make([]attachmentDoc, len(attachments))
	for i, a := range attachments {
		docs[i] = attachmentDoc{
			ID:          a.GetID(),
			Filename:    a.GetFilename(),
			ContentType: a.GetContentType(),
			Size:        a.GetSize(),
			URI:         a.GetURI(),
			CreatedAt:   a.GetCreatedAt(),
		}
	}

	return json.Marshal(docs)
}

func (s *Store) unmarshalAttachments(data []byte) ([]store.Attachment, error) {
	var docs []attachmentDoc
	if err := json.Unmarshal(data, &docs); err != nil {
		return nil, err
	}

	attachments := make([]store.Attachment, len(docs))
	for i, d := range docs {
		attachments[i] = &attachment{
			id:          d.ID,
			filename:    d.Filename,
			contentType: d.ContentType,
			size:        d.Size,
			uri:         d.URI,
			createdAt:   d.CreatedAt,
		}
	}

	return attachments, nil
}
