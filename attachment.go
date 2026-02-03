package mailbox

import (
	"context"
	"fmt"
	"io"

	"github.com/rbaliyan/mailbox/store"
)

// AttachmentStore is an alias for store.AttachmentFileStore.
// Deprecated: Use store.AttachmentFileStore directly.
type AttachmentStore = store.AttachmentFileStore

// attachmentManager implements store.AttachmentManager with reference counting.
type attachmentManager struct {
	metadata store.AttachmentMetadataStore
	files    store.AttachmentFileStore
}

// NewAttachmentManager creates an attachment manager with reference-counted deletion.
func NewAttachmentManager(metadata store.AttachmentMetadataStore, files store.AttachmentFileStore) store.AttachmentManager {
	return &attachmentManager{
		metadata: metadata,
		files:    files,
	}
}

// GetMetadata retrieves attachment metadata by ID.
func (m *attachmentManager) GetMetadata(ctx context.Context, id string) (store.AttachmentMetadata, error) {
	return m.metadata.Get(ctx, id)
}

// Upload uploads a file and creates metadata with ref count of 0.
// If an attachment with the same hash exists, returns existing metadata (deduplication).
//
// IMPORTANT: Upload does NOT increment the reference count. The caller is responsible
// for calling AddRef() for each message that uses the attachment. This allows the same
// attachment to be shared across multiple messages with proper reference tracking.
//
// Typical usage:
//
//	meta, err := manager.Upload(ctx, "file.pdf", "application/pdf", hash, reader)
//	if err != nil { ... }
//	// Add to draft, then when message is created:
//	manager.AddRef(ctx, meta.GetID())
func (m *attachmentManager) Upload(ctx context.Context, filename, contentType, hash string, content io.Reader) (store.AttachmentMetadata, error) {
	// Check for existing attachment with same hash (deduplication)
	if hash != "" {
		existing, err := m.metadata.GetByHash(ctx, hash)
		if err == nil {
			// Found existing attachment, reuse it
			return existing, nil
		}
		// If not found, continue to upload
	}

	// Upload the file
	uri, err := m.files.Upload(ctx, filename, contentType, content)
	if err != nil {
		return nil, fmt.Errorf("upload file: %w", err)
	}

	// Create metadata
	meta := m.metadata.NewAttachmentMetadata()
	meta.SetFilename(filename)
	meta.SetContentType(contentType)
	meta.SetURI(uri)
	meta.SetHash(hash)
	meta.SetRefCount(0)

	if err := m.metadata.Create(ctx, meta); err != nil {
		// Try to clean up uploaded file
		_ = m.files.Delete(ctx, uri)
		return nil, fmt.Errorf("create metadata: %w", err)
	}

	return meta, nil
}

// Load returns a reader for the attachment content.
func (m *attachmentManager) Load(ctx context.Context, id string) (io.ReadCloser, error) {
	meta, err := m.metadata.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get metadata: %w", err)
	}
	return m.files.Load(ctx, meta.GetURI())
}

// AddRef increments the reference count for an attachment.
func (m *attachmentManager) AddRef(ctx context.Context, id string) error {
	return m.metadata.IncrementRef(ctx, id)
}

// RemoveRef decrements the reference count and deletes if no references remain.
// Uses atomic decrement-and-delete to prevent race conditions.
// Safe to call multiple times - will not go negative.
func (m *attachmentManager) RemoveRef(ctx context.Context, id string) error {
	// Atomically decrement and delete if count reaches zero
	// This prevents race conditions where two concurrent releases
	// both see count=1 and both try to delete
	deleted, uri, err := m.metadata.DecrementRefAndDeleteIfZero(ctx, id)
	if err != nil {
		return fmt.Errorf("release attachment: %w", err)
	}

	if deleted && uri != "" {
		// Metadata was deleted, now delete the file
		// File deletion can fail without causing inconsistency -
		// worst case is orphaned file that can be cleaned up later
		if err := m.files.Delete(ctx, uri); err != nil {
			return fmt.Errorf("delete file: %w", err)
		}
	}

	return nil
}
