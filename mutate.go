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
		if err := m.publishMessageMoved(ctx, messageID, m.userID, oldFolderID, folderID, !msg.GetIsRead()); err != nil {
			return err
		}
	}

	// Apply read flag after archive (less severe failure mode if this fails)
	if flags.Read != nil {
		if err := m.service.store.MarkRead(ctx, messageID, *flags.Read); err != nil {
			return fmt.Errorf("mark read: %w", err)
		}
	}

	// Track the current folder ID for the read event.
	// If the archive flag changed the folder above, use the new folder.
	currentFolderID := msg.GetFolderID()
	if flags.Archived != nil {
		if *flags.Archived {
			currentFolderID = store.FolderArchived
		} else {
			currentFolderID = defaultFolderForMessage(msg)
		}
	}

	// Publish read event (only for marking as read, not unread)
	if flags.Read != nil && *flags.Read {
		if err := m.service.events.MessageRead.Publish(ctx, MessageReadEvent{
			MessageID: messageID,
			UserID:    m.userID,
			FolderID:  currentFolderID,
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
	if err := m.publishMessageMoved(ctx, messageID, m.userID, oldFolderID, store.FolderTrash, !msg.GetIsRead()); err != nil {
		return err
	}

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
	if err := m.publishMessageMoved(ctx, messageID, m.userID, store.FolderTrash, folderID, !msg.GetIsRead()); err != nil {
		return err
	}

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
	if err := m.publishMessageMoved(ctx, messageID, m.userID, oldFolderID, folderID, !msg.GetIsRead()); err != nil {
		return err
	}

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

// Helper methods

func (m *userMailbox) canAccess(msg store.Message) bool {
	return msg.GetOwnerID() == m.userID
}

// publishMessageMoved publishes a MessageMoved event.
// Returns an EventPublishError when eventErrorsFatal is true and publishing fails.
// Otherwise, logs the failure and returns nil.
func (m *userMailbox) publishMessageMoved(ctx context.Context, messageID, userID, fromFolderID, toFolderID string, wasUnread bool) error {
	if fromFolderID == toFolderID {
		return nil
	}
	if err := m.service.events.MessageMoved.Publish(ctx, MessageMovedEvent{
		MessageID:    messageID,
		UserID:       userID,
		FromFolderID: fromFolderID,
		ToFolderID:   toFolderID,
		WasUnread:    wasUnread,
		MovedAt:      time.Now().UTC(),
	}); err != nil {
		if m.service.opts.eventErrorsFatal {
			return &EventPublishError{
				Event:     "MessageMoved",
				MessageID: messageID,
				Err:       err,
			}
		}
		m.service.opts.safeEventPublishFailure("MessageMoved", err)
	}
	return nil
}
