package mailbox

import (
	"context"
	"fmt"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// defaultFolderForMessage returns the default folder for a message based on ownership.
// Sent messages go to the sent folder, received messages go to the inbox.
func defaultFolderForMessage(msg store.Message) string {
	if store.IsSentByOwner(msg.GetOwnerID(), msg.GetSenderID()) {
		return store.FolderSent
	}
	return store.FolderInbox
}

// UpdateFlags updates message flags (read status, archived status).
func (m *userMailbox) UpdateFlags(ctx context.Context, messageID string, flags Flags) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordUpdate(ctx, time.Since(start), "update_flags", retErr) }()

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	// Apply archived flag first (moves to/from archived folder).
	// This is the more complex operation and is done first so that if it fails,
	// no rollback of the read flag is needed.
	if flags.Archived != nil {
		oldFolderID := msg.GetFolderID()
		var folderID string
		if *flags.Archived {
			folderID = store.FolderArchived
		} else {
			folderID = defaultFolderForMessage(msg)
		}
		if err := m.service.store.MoveToFolder(ctx, messageID, folderID); err != nil {
			return fmt.Errorf("move to folder: %w", err)
		}

		// Publish move event
		m.publishMessageMoved(ctx, messageID, m.userID, oldFolderID, folderID)
	}

	// Apply read flag after archive (less severe failure mode if this fails)
	if flags.Read != nil {
		if err := m.service.store.MarkRead(ctx, messageID, *flags.Read); err != nil {
			return fmt.Errorf("mark read: %w", err)
		}
	}

	// Publish read event (only for marking as read, not unread)
	if flags.Read != nil && *flags.Read {
		if err := m.service.events.MessageRead.Publish(ctx, MessageReadEvent{
			MessageID: messageID,
			UserID:    m.userID,
			FolderID:  msg.GetFolderID(),
			ReadAt:    time.Now().UTC(),
		}); err != nil {
			if m.service.opts.eventErrorsFatal {
				// Operation succeeded but event failed - return EventPublishError
				return &EventPublishError{
					Event:     "MessageRead",
					MessageID: messageID,
					Err:       err,
				}
			}
			m.service.opts.safeEventPublishFailure("MessageRead", err)
		}
	}

	return nil
}

// Delete moves a message to trash.
func (m *userMailbox) Delete(ctx context.Context, messageID string) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordDelete(ctx, time.Since(start), false, retErr) }()

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if msg.GetFolderID() == store.FolderTrash {
		return ErrAlreadyInTrash
	}

	oldFolderID := msg.GetFolderID()
	if err := m.service.store.MoveToFolder(ctx, messageID, store.FolderTrash); err != nil {
		return fmt.Errorf("move to trash: %w", err)
	}

	// Publish move event
	m.publishMessageMoved(ctx, messageID, m.userID, oldFolderID, store.FolderTrash)

	return nil
}

// Restore restores a message from trash.
func (m *userMailbox) Restore(ctx context.Context, messageID string) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordUpdate(ctx, time.Since(start), "restore", retErr) }()

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if msg.GetFolderID() != store.FolderTrash {
		return ErrNotInTrash
	}

	folderID := defaultFolderForMessage(msg)

	if err := m.service.store.MoveToFolder(ctx, messageID, folderID); err != nil {
		return fmt.Errorf("restore message: %w", err)
	}

	// Publish move event
	m.publishMessageMoved(ctx, messageID, m.userID, store.FolderTrash, folderID)

	return nil
}

// PermanentlyDelete permanently deletes a message from trash.
func (m *userMailbox) PermanentlyDelete(ctx context.Context, messageID string) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordDelete(ctx, time.Since(start), true, retErr) }()

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if msg.GetFolderID() != store.FolderTrash {
		return ErrNotInTrash
	}

	// Hard delete the message FIRST to avoid race condition.
	// If we released attachment refs first and then delete failed,
	// another process could see refs=0 and delete the attachments
	// while the message still exists.
	if err := m.service.store.HardDelete(ctx, messageID); err != nil {
		return fmt.Errorf("hard delete message: %w", err)
	}

	// Release attachment references AFTER successful delete.
	// If this fails, we have orphaned attachments (better than missing attachments on existing messages).
	if err := m.releaseAttachmentRefs(ctx, msg); err != nil {
		m.service.logger.Error("failed to release attachment refs during permanent delete - attachments may be orphaned",
			"error", err, "message_id", messageID)
	}

	// Publish event
	if err := m.service.events.MessageDeleted.Publish(ctx, MessageDeletedEvent{
		MessageID: messageID,
		UserID:    m.userID,
		FolderID:  msg.GetFolderID(),
		WasUnread: !msg.GetIsRead(),
		DeletedAt: time.Now().UTC(),
	}); err != nil {
		if m.service.opts.eventErrorsFatal {
			// Operation succeeded but event failed - return EventPublishError
			return &EventPublishError{
				Event:     "MessageDeleted",
				MessageID: messageID,
				Err:       err,
			}
		}
		m.service.opts.safeEventPublishFailure("MessageDeleted", err)
	}

	return nil
}

// MoveToFolder moves a message to a folder.
func (m *userMailbox) MoveToFolder(ctx context.Context, messageID, folderID string) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordMove(ctx, time.Since(start), folderID, retErr) }()

	// Validate folder ID
	if !store.IsValidFolderID(folderID) {
		return fmt.Errorf("%w: %s", ErrInvalidFolderID, folderID)
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	oldFolderID := msg.GetFolderID()
	if err := m.service.store.MoveToFolder(ctx, messageID, folderID); err != nil {
		return fmt.Errorf("move to folder: %w", err)
	}

	// Publish move event
	m.publishMessageMoved(ctx, messageID, m.userID, oldFolderID, folderID)

	return nil
}

// AddTag adds a tag to a message.
func (m *userMailbox) AddTag(ctx context.Context, messageID, tagID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	if tagID == "" {
		return fmt.Errorf("%w: empty tag ID", ErrInvalidID)
	}
	if len(tagID) > MaxTagIDLength {
		return fmt.Errorf("%w: tag ID exceeds maximum length of %d", ErrInvalidID, MaxTagIDLength)
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if err := m.service.store.AddTag(ctx, messageID, tagID); err != nil {
		return fmt.Errorf("add tag: %w", err)
	}

	return nil
}

// RemoveTag removes a tag from a message.
func (m *userMailbox) RemoveTag(ctx context.Context, messageID, tagID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	if tagID == "" {
		return fmt.Errorf("%w: empty tag ID", ErrInvalidID)
	}
	if len(tagID) > MaxTagIDLength {
		return fmt.Errorf("%w: tag ID exceeds maximum length of %d", ErrInvalidID, MaxTagIDLength)
	}

	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}

	if !m.canAccess(msg) {
		return ErrUnauthorized
	}

	if err := m.service.store.RemoveTag(ctx, messageID, tagID); err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}

	return nil
}

// BulkUpdateFlags updates flags on multiple messages by ID.
func (m *userMailbox) BulkUpdateFlags(ctx context.Context, messageIDs []string, flags Flags) (*BulkResult, error) {
	return m.bulkOp(ctx, messageIDs, func(id string) error {
		return m.UpdateFlags(ctx, id, flags)
	})
}

// BulkMove moves multiple messages to a folder by ID.
func (m *userMailbox) BulkMove(ctx context.Context, messageIDs []string, folderID string) (*BulkResult, error) {
	return m.bulkOp(ctx, messageIDs, func(id string) error {
		return m.MoveToFolder(ctx, id, folderID)
	})
}

// BulkDelete moves multiple messages to trash by ID.
func (m *userMailbox) BulkDelete(ctx context.Context, messageIDs []string) (*BulkResult, error) {
	return m.bulkOp(ctx, messageIDs, func(id string) error {
		return m.Delete(ctx, id)
	})
}

// BulkAddTag adds a tag to multiple messages by ID.
func (m *userMailbox) BulkAddTag(ctx context.Context, messageIDs []string, tagID string) (*BulkResult, error) {
	return m.bulkOp(ctx, messageIDs, func(id string) error {
		return m.AddTag(ctx, id, tagID)
	})
}

// BulkRemoveTag removes a tag from multiple messages by ID.
func (m *userMailbox) BulkRemoveTag(ctx context.Context, messageIDs []string, tagID string) (*BulkResult, error) {
	return m.bulkOp(ctx, messageIDs, func(id string) error {
		return m.RemoveTag(ctx, id, tagID)
	})
}

// bulkOp applies an operation to each message ID, collecting results.
// Checks for context cancellation between iterations to support early termination.
func (m *userMailbox) bulkOp(ctx context.Context, messageIDs []string, op func(id string) error) (*BulkResult, error) {
	if err := m.checkAccess(); err != nil {
		return nil, err
	}

	result := &BulkResult{Results: make([]OperationResult, 0, len(messageIDs))}
	for i, id := range messageIDs {
		if err := ctx.Err(); err != nil {
			// Batch-append all remaining items as cancelled and break
			for _, remaining := range messageIDs[i:] {
				result.Results = append(result.Results, OperationResult{ID: remaining, Error: err})
			}
			break
		}
		res := OperationResult{ID: id}
		if err := op(id); err != nil {
			res.Error = err
		} else {
			res.Success = true
		}
		result.Results = append(result.Results, res)
	}
	return result, result.Err()
}

// Helper methods

func (m *userMailbox) canAccess(msg store.Message) bool {
	return msg.GetOwnerID() == m.userID
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

// publishMessageMoved publishes a MessageMoved event, swallowing errors.
func (m *userMailbox) publishMessageMoved(ctx context.Context, messageID, userID, fromFolderID, toFolderID string) {
	if fromFolderID == toFolderID {
		return
	}
	if err := m.service.events.MessageMoved.Publish(ctx, MessageMovedEvent{
		MessageID:    messageID,
		UserID:       userID,
		FromFolderID: fromFolderID,
		ToFolderID:   toFolderID,
		MovedAt:      time.Now().UTC(),
	}); err != nil {
		if m.service.opts.eventErrorsFatal {
			m.service.logger.Error("failed to publish MessageMoved event", "error", err, "message_id", messageID)
		} else {
			m.service.opts.safeEventPublishFailure("MessageMoved", err)
		}
	}
}
