package store

import (
	"time"
)

// MessageStatus represents the status of a message.
type MessageStatus string

// Message status constants.
const (
	MessageStatusDraft     MessageStatus = "draft"
	MessageStatusQueued    MessageStatus = "queued"
	MessageStatusSent      MessageStatus = "sent"
	MessageStatusDelivered MessageStatus = "delivered"
	MessageStatusFailed    MessageStatus = "failed"
)

// Reserved folder names.
// All messages belong to a folder. Use these constants for system folders.
// Reserved folders start with "__" prefix - user-defined folders must not use this prefix.
const (
	FolderInbox    = "__inbox"
	FolderSent     = "__sent"
	FolderArchived = "__archived"
	FolderTrash    = "__trash"
	FolderSpam     = "__spam"
	FolderOutbox   = "__outbox"
	FolderDrafts   = "__drafts"

	// FolderPrefix is the prefix for reserved system folders.
	FolderPrefix = "__"
)

// reservedFolders is the set of valid reserved folder IDs.
var reservedFolders = map[string]bool{
	FolderInbox:    true,
	FolderSent:     true,
	FolderArchived: true,
	FolderTrash:    true,
	FolderSpam:     true,
	FolderOutbox:   true,
	FolderDrafts:   true,
}

// IsReservedFolder returns true if the folder ID is a reserved system folder.
func IsReservedFolder(folderID string) bool {
	return reservedFolders[folderID]
}

// IsValidFolderID validates a folder ID.
// Returns true if the folder ID is either:
// - A valid reserved folder (starts with "__" and is in the known set)
// - A valid user-defined folder (non-empty, doesn't start with "__")
func IsValidFolderID(folderID string) bool {
	if folderID == "" {
		return false
	}
	// If it starts with reserved prefix, must be a known reserved folder
	if len(folderID) >= 2 && folderID[:2] == FolderPrefix {
		return reservedFolders[folderID]
	}
	// User-defined folder - just needs to be non-empty (already checked)
	return true
}

// IsSentByOwner returns true if the message was sent by its owner.
// ownerID == senderID means "sent", otherwise "received".
func IsSentByOwner(ownerID, senderID string) bool {
	return ownerID == senderID
}

// Attachment is the interface for attachment data.
type Attachment interface {
	GetID() string
	GetFilename() string
	GetContentType() string
	GetSize() int64
	GetURI() string
	GetCreatedAt() time.Time
}

// MessageReader provides read access to the fields shared by both
// sent messages and drafts: identity, content, and timestamps.
type MessageReader interface {
	GetID() string
	GetOwnerID() string
	GetSenderID() string
	GetSubject() string
	GetBody() string
	GetRecipientIDs() []string
	GetMetadata() map[string]any
	GetAttachments() []Attachment
	GetCreatedAt() time.Time
	GetUpdatedAt() time.Time
}

// Message is a read-only view of a sent or received message.
// Messages cannot be directly modified - use specific Store operations
// like MarkRead, MoveToFolder, AddTag, etc.
type Message interface {
	MessageReader

	GetStatus() MessageStatus
	GetIsRead() bool
	GetReadAt() *time.Time
	GetFolderID() string
	GetTags() []string

	// Thread support
	GetThreadID() string
	GetReplyToID() string
}

// DraftMessage is a mutable message being composed.
// Drafts can only be created via Store.NewDraft() and are always
// owned by a single user. They cannot be moved to folders, marked
// as read, or tagged - those operations only apply to sent messages.
type DraftMessage interface {
	MessageReader

	// Write operations (fluent API)
	SetSubject(subject string) DraftMessage
	SetBody(body string) DraftMessage
	SetRecipients(recipientIDs ...string) DraftMessage
	SetMetadata(key string, value any) DraftMessage
	AddAttachment(attachment Attachment) DraftMessage
}

// MessageData contains data for creating a new message.
// Used internally when sending a draft to create message copies.
type MessageData struct {
	OwnerID      string
	SenderID     string
	RecipientIDs []string
	Subject      string
	Body         string
	Metadata     map[string]any
	Status       MessageStatus
	FolderID     string
	Attachments  []Attachment
	Tags         []string
	ThreadID     string
	ReplyToID    string
}

// MessageList represents a paginated list of messages.
type MessageList struct {
	Messages   []Message
	Total      int64
	HasMore    bool
	NextCursor string
}

// DraftList represents a paginated list of drafts.
type DraftList struct {
	Drafts     []DraftMessage
	Total      int64
	HasMore    bool
	NextCursor string
}
