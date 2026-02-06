package mailbox

import (
	"context"
	"fmt"
	"io"

	"github.com/rbaliyan/mailbox/store"
)

// LoadAttachment loads attachment content by message and attachment ID.
func (m *userMailbox) LoadAttachment(ctx context.Context, messageID, attachmentID string) (io.ReadCloser, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	if m.service.attachments == nil {
		return nil, ErrAttachmentStoreNotConfigured
	}

	// Get the message to verify access and find attachment
	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return nil, ErrUnauthorized
	}

	// Verify the attachment belongs to this message
	attachments := msg.GetAttachments()
	var found bool
	for _, a := range attachments {
		if a.GetID() == attachmentID {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrAttachmentNotFound
	}

	// Load content from attachment manager
	return m.service.attachments.Load(ctx, attachmentID)
}

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
	meta, err := m.metadata.Create(ctx, store.AttachmentCreate{
		Filename:    filename,
		ContentType: contentType,
		URI:         uri,
		Hash:        hash,
	})
	if err != nil {
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

// addAttachmentRefs increments reference counts for all attachments in a message.
// On failure, it rolls back any refs that were successfully added.
func (m *userMailbox) addAttachmentRefs(ctx context.Context, msg store.Message) error {
	if m.service.attachments == nil {
		return nil
	}

	attachments := msg.GetAttachments()
	added := make([]string, 0, len(attachments))

	for _, a := range attachments {
		if err := m.service.attachments.AddRef(ctx, a.GetID()); err != nil {
			m.service.logger.Warn("failed to add attachment ref", "error", err, "attachment_id", a.GetID())
			// Rollback: release refs for attachments we already added
			var rollbackFailed map[string]error
			for _, addedID := range added {
				if releaseErr := m.service.attachments.RemoveRef(ctx, addedID); releaseErr != nil {
					m.service.logger.Warn("failed to rollback attachment ref", "error", releaseErr, "attachment_id", addedID)
					if rollbackFailed == nil {
						rollbackFailed = make(map[string]error)
					}
					rollbackFailed[addedID] = releaseErr
				}
			}
			return &AttachmentRefError{
				Operation:      "add",
				Failed:         map[string]error{a.GetID(): err},
				RollbackFailed: rollbackFailed,
			}
		}
		added = append(added, a.GetID())
	}
	return nil
}

// releaseAttachmentRefs decrements reference counts for all attachments in a message.
// Returns an error if any ref releases fail (but continues processing all).
func (m *userMailbox) releaseAttachmentRefs(ctx context.Context, msg store.Message) error {
	if m.service.attachments == nil {
		return nil
	}
	var failed map[string]error
	for _, a := range msg.GetAttachments() {
		if err := m.service.attachments.RemoveRef(ctx, a.GetID()); err != nil {
			m.service.logger.Warn("failed to release attachment ref", "error", err, "attachment_id", a.GetID())
			if failed == nil {
				failed = make(map[string]error)
			}
			failed[a.GetID()] = err
		}
	}
	if len(failed) > 0 {
		return &AttachmentRefError{Operation: "release", Failed: failed}
	}
	return nil
}
