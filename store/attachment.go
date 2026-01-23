package store

import (
	"context"
	"io"
	"time"
)

// AttachmentMetadata extends Attachment with reference tracking.
// This is stored separately from messages to enable safe deletion.
type AttachmentMetadata interface {
	Attachment

	// GetHash returns the content hash for deduplication.
	GetHash() string

	// GetRefCount returns the number of messages referencing this attachment.
	GetRefCount() int
}

// MutableAttachmentMetadata extends AttachmentMetadata with mutation methods.
type MutableAttachmentMetadata interface {
	AttachmentMetadata

	SetID(id string)
	SetFilename(filename string)
	SetContentType(contentType string)
	SetSize(size int64)
	SetURI(uri string)
	SetHash(hash string)
	SetRefCount(count int)
	SetCreatedAt(t time.Time)
}

// AttachmentMetadataStore manages attachment metadata with reference counting.
// This enables safe deletion of attachment files when no messages reference them.
type AttachmentMetadataStore interface {
	// Create stores new attachment metadata with initial ref count of 0.
	Create(ctx context.Context, meta MutableAttachmentMetadata) error

	// Get retrieves attachment metadata by ID.
	Get(ctx context.Context, id string) (MutableAttachmentMetadata, error)

	// GetByHash finds attachment by content hash for deduplication.
	// Returns ErrNotFound if no attachment with the hash exists.
	GetByHash(ctx context.Context, hash string) (MutableAttachmentMetadata, error)

	// IncrementRef atomically increments the reference count.
	// Called when a message with this attachment is created.
	IncrementRef(ctx context.Context, id string) error

	// DecrementRef atomically decrements the reference count.
	// Returns the new count. Called when a message is permanently deleted.
	// Deprecated: Use DecrementRefAndDeleteIfZero for atomic release.
	DecrementRef(ctx context.Context, id string) (newCount int, err error)

	// DecrementRefAndDeleteIfZero atomically decrements the reference count
	// and deletes the metadata if the count reaches zero.
	// Returns (true, uri) if deleted, (false, "") if not deleted.
	// This MUST be atomic to prevent race conditions where two concurrent
	// releases both see count=1 and both try to delete.
	//
	// For MongoDB: Use findOneAndUpdate with $inc and return the document,
	//              then delete in same transaction if count <= 0.
	// For PostgreSQL: Use DELETE ... WHERE id = $1 AND ref_count <= 1 RETURNING uri,
	//                 or UPDATE + DELETE in a transaction.
	DecrementRefAndDeleteIfZero(ctx context.Context, id string) (deleted bool, uri string, err error)

	// Delete removes attachment metadata.
	// Should only be called when ref count is 0.
	Delete(ctx context.Context, id string) error

	// NewAttachmentMetadata creates a new mutable attachment metadata instance.
	NewAttachmentMetadata() MutableAttachmentMetadata
}

// AttachmentFileStore handles the actual file storage operations.
// Implementations can support S3, GCS, local filesystem, GridFS, etc.
type AttachmentFileStore interface {
	// Upload stores content and returns a URI for later retrieval.
	Upload(ctx context.Context, filename, contentType string, content io.Reader) (uri string, err error)

	// Load returns a reader for the attachment content.
	// Caller is responsible for closing the reader.
	Load(ctx context.Context, uri string) (io.ReadCloser, error)

	// Delete removes the attachment file from storage.
	Delete(ctx context.Context, uri string) error
}

// AttachmentManager combines metadata and file storage with reference-counted deletion.
// This is the recommended way to manage attachments.
type AttachmentManager interface {
	// Upload uploads a file and creates metadata with the given hash.
	// If an attachment with the same hash exists, returns existing metadata
	// without uploading (deduplication).
	Upload(ctx context.Context, filename, contentType, hash string, content io.Reader) (AttachmentMetadata, error)

	// Load returns a reader for the attachment content.
	Load(ctx context.Context, id string) (io.ReadCloser, error)

	// AddRef increments the reference count for an attachment.
	// Called when a message with this attachment is created.
	AddRef(ctx context.Context, id string) error

	// RemoveRef decrements the reference count for an attachment.
	// If the count reaches 0, the file and metadata are deleted.
	// Called when a message is permanently deleted.
	RemoveRef(ctx context.Context, id string) error
}
