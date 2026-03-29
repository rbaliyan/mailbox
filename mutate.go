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

	// No flags set — nothing to do. This returns early before getAndVerify,
	// so a nonexistent messageID will return nil (not ErrNotFound). This is
	// intentional: an empty update is a no-op regardless of message existence.
	if flags.Read == nil && flags.Archived == nil {
		return nil
	}

	start := time.Now()
	defer func() { m.service.otel.recordUpdate(ctx, time.Since(start), "update_flags", retErr) }()

	msg, err := m.getAndVerify(ctx, messageID)
	if err != nil {
		return err
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
		if err := m.moveAndPublish(ctx, messageID, m.userID, oldFolderID, folderID, !msg.GetIsRead()); err != nil {
			return fmt.Errorf("move to folder: %w", err)
		}
	}

	// Track the current folder ID for the read event.
	currentFolderID := msg.GetFolderID()
	if flags.Archived != nil {
		if *flags.Archived {
			currentFolderID = store.FolderArchived
		} else {
			currentFolderID = defaultFolderForMessage(msg)
		}
	}

	// Apply read flag with atomic event publishing.
	// Event only fires on mark-as-read (not unread) — MessageReadEvent has no IsRead field.
	if flags.Read != nil {
		var events []any
		if *flags.Read {
			events = []any{MessageReadEvent{
				MessageID: messageID,
				UserID:    m.userID,
				FolderID:  currentFolderID,
				ReadAt:    time.Now().UTC(),
			}}
		}
		op := func(txCtx context.Context) error {
			return m.service.store.MarkRead(txCtx, messageID, *flags.Read)
		}
		if len(events) > 0 {
			if err := m.service.opAndPublish(ctx, EventNameMessageRead, messageID, events[0], op); err != nil {
				return fmt.Errorf("mark read: %w", err)
			}
		} else {
			if err := op(ctx); err != nil {
				return fmt.Errorf("mark unread: %w", err)
			}
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

	msg, err := m.getAndVerify(ctx, messageID)
	if err != nil {
		return err
	}

	if msg.GetFolderID() == store.FolderTrash {
		return ErrAlreadyInTrash
	}

	oldFolderID := msg.GetFolderID()
	if err := m.moveAndPublish(ctx, messageID, m.userID, oldFolderID, store.FolderTrash, !msg.GetIsRead()); err != nil {
		return fmt.Errorf("move to trash: %w", err)
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

	msg, err := m.getAndVerify(ctx, messageID)
	if err != nil {
		return err
	}

	if msg.GetFolderID() != store.FolderTrash {
		return ErrNotInTrash
	}

	folderID := defaultFolderForMessage(msg)

	if err := m.moveAndPublish(ctx, messageID, m.userID, store.FolderTrash, folderID, !msg.GetIsRead()); err != nil {
		return fmt.Errorf("restore message: %w", err)
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

	msg, err := m.getAndVerify(ctx, messageID)
	if err != nil {
		return err
	}

	if msg.GetFolderID() != store.FolderTrash {
		return ErrNotInTrash
	}

	// Hard delete + event atomically.
	if err := m.service.opAndPublish(ctx, EventNameMessageDeleted, messageID,
		MessageDeletedEvent{
			MessageID: messageID,
			UserID:    m.userID,
			FolderID:  msg.GetFolderID(),
			WasUnread: !msg.GetIsRead(),
			DeletedAt: time.Now().UTC(),
		},
		func(txCtx context.Context) error {
			return m.service.store.HardDelete(txCtx, messageID)
		},
	); err != nil {
		return fmt.Errorf("hard delete message: %w", err)
	}

	// Release attachment references AFTER successful delete.
	if err := m.releaseAttachmentRefs(ctx, msg); err != nil {
		m.service.logger.Error("failed to release attachment refs during permanent delete - attachments may be orphaned",
			"error", err, "message_id", messageID)
	}

	return nil
}

// MoveToFolder moves a message to a folder.
func (m *userMailbox) MoveToFolder(ctx context.Context, messageID, folderID string, opts ...store.MoveOption) (retErr error) {
	if err := m.checkAccess(); err != nil {
		return err
	}

	start := time.Now()
	defer func() { m.service.otel.recordMove(ctx, time.Since(start), folderID, retErr) }()

	// Validate folder ID
	if !store.IsValidFolderID(folderID) {
		return fmt.Errorf("%w: %s", ErrInvalidFolderID, folderID)
	}

	// Validate fromFolderID if provided
	mo := store.ApplyMoveOptions(opts)
	if from := mo.FromFolderID(); from != "" && !store.IsValidFolderID(from) {
		return fmt.Errorf("%w: %s", ErrInvalidFolderID, from)
	}

	msg, err := m.getAndVerify(ctx, messageID)
	if err != nil {
		return err
	}

	oldFolderID := msg.GetFolderID()
	return m.moveAndPublish(ctx, messageID, m.userID, oldFolderID, folderID, !msg.GetIsRead(), opts...)
}

// AddTag adds a tag to a message.
func (m *userMailbox) AddTag(ctx context.Context, messageID, tagID string) error {
	if err := m.checkAccess(); err != nil {
		return err
	}

	if tagID == "" {
		return fmt.Errorf("%w: empty tag ID", ErrInvalidID)
	}
	if !isValidTagID(tagID) {
		return fmt.Errorf("%w: tag ID contains invalid characters", ErrInvalidID)
	}
	if len(tagID) > MaxTagIDLength {
		return fmt.Errorf("%w: tag ID exceeds maximum length of %d", ErrInvalidID, MaxTagIDLength)
	}

	if _, err := m.getAndVerify(ctx, messageID); err != nil {
		return err
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
	if !isValidTagID(tagID) {
		return fmt.Errorf("%w: tag ID contains invalid characters", ErrInvalidID)
	}
	if len(tagID) > MaxTagIDLength {
		return fmt.Errorf("%w: tag ID exceeds maximum length of %d", ErrInvalidID, MaxTagIDLength)
	}

	if _, err := m.getAndVerify(ctx, messageID); err != nil {
		return err
	}

	if err := m.service.store.RemoveTag(ctx, messageID, tagID); err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}

	return nil
}

// MarkAllRead marks all unread messages in a folder as read.
// Uses store.BulkReadMarker for a single database operation when available,
// falling back to individual MarkRead calls otherwise.
func (m *userMailbox) MarkAllRead(ctx context.Context, folderID string) (_ int64, retErr error) {
	if err := m.checkAccess(); err != nil {
		return 0, err
	}

	start := time.Now()
	defer func() { m.service.otel.recordUpdate(ctx, time.Since(start), "mark_all_read", retErr) }()

	var count int64

	// Fast path: use BulkReadMarker if the store supports it.
	if brm, ok := m.service.store.(store.BulkReadMarker); ok {
		var err error
		count, err = brm.MarkAllRead(ctx, m.userID, folderID)
		if err != nil {
			return 0, fmt.Errorf("mark all read: %w", err)
		}
	} else {
		// Slow path: individual MarkRead calls with cursor pagination.
		filters := []store.Filter{
			store.OwnerIs(m.userID),
			store.InFolder(folderID),
			store.IsReadFilter(false),
		}
		var cursor string
		for {
			list, err := m.service.store.Find(ctx, filters, store.ListOptions{
				Limit:      m.service.opts.maxQueryLimit,
				StartAfter: cursor,
			})
			if err != nil {
				return count, fmt.Errorf("find unread: %w", err)
			}
			for _, msg := range list.Messages {
				if err := m.service.store.MarkRead(ctx, msg.GetID(), true); err != nil {
					m.service.logger.Warn("mark read failed in bulk operation",
						"error", err, "message_id", msg.GetID())
				} else {
					count++
				}
			}
			if !list.HasMore || len(list.Messages) == 0 {
				break
			}
			cursor = list.Messages[len(list.Messages)-1].GetID()
		}
	}

	// Update stats cache directly — skip per-message events for bulk path.
	if count > 0 {
		m.service.updateCachedStats(m.userID, func(stats *store.MailboxStats) {
			if stats.UnreadCount >= count {
				stats.UnreadCount -= count
			} else {
				stats.UnreadCount = 0
			}
			c := stats.Folders[folderID]
			if c.Unread >= count {
				c.Unread -= count
			} else {
				c.Unread = 0
			}
			stats.Folders[folderID] = c
		})

		// Publish MarkAllRead event (DB write already done above).
		if err := m.service.publishOnly(ctx, EventNameMarkAllRead, folderID,
			MarkAllReadEvent{
				UserID:   m.userID,
				FolderID: folderID,
				Count:    count,
				MarkedAt: time.Now().UTC(),
			},
		); err != nil {
			return count, err
		}
	}

	return count, nil
}

// Helper methods

func (m *userMailbox) canAccess(msg store.Message) bool {
	return msg.GetOwnerID() == m.userID
}

// getAndVerify retrieves a message and verifies the current user owns it.
// This centralizes the get-check-authorize pattern used by all mutation methods.
func (m *userMailbox) getAndVerify(ctx context.Context, messageID string) (store.Message, error) {
	msg, err := m.service.store.Get(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	if !m.canAccess(msg) {
		return nil, ErrUnauthorized
	}
	return msg, nil
}

// moveAndPublish moves a message to a folder and publishes the MessageMoved event.
// When the store supports outbox, both operations are atomic (single transaction).
// Otherwise, the event is published directly (best-effort, current behavior).
func (m *userMailbox) moveAndPublish(ctx context.Context, messageID, userID, fromFolderID, toFolderID string, wasUnread bool, opts ...store.MoveOption) error {
	if fromFolderID == toFolderID {
		return nil
	}
	return m.service.opAndPublish(ctx, EventNameMessageMoved, messageID,
		MessageMovedEvent{
			MessageID:    messageID,
			UserID:       userID,
			FromFolderID: fromFolderID,
			ToFolderID:   toFolderID,
			WasUnread:    wasUnread,
			MovedAt:      time.Now().UTC(),
		},
		func(txCtx context.Context) error {
			return m.service.store.MoveToFolder(txCtx, messageID, toFolderID, opts...)
		},
	)
}

// --- Filter-based bulk operations ---

// scopedFilters prepends owner and non-draft filters to the caller's filters.
func (m *userMailbox) scopedFilters(filters []store.Filter) []store.Filter {
	scoped := make([]store.Filter, 0, len(filters)+2)
	scoped = append(scoped, store.OwnerIs(m.userID), store.IsDraftFilter(false))
	scoped = append(scoped, filters...)
	return scoped
}

func (m *userMailbox) UpdateByFilter(ctx context.Context, filters []store.Filter, flags Flags) (int64, error) {
	if err := m.checkAccess(); err != nil {
		return 0, err
	}
	scoped := m.scopedFilters(filters)

	// Fast path: native bulk update.
	if bu, ok := m.service.store.(store.BulkUpdater); ok && flags.Read != nil {
		return bu.MarkReadByFilter(ctx, m.userID, scoped, *flags.Read)
	}

	// Slow path: paginated iteration.
	var count int64
	var cursor string
	for {
		list, err := m.service.store.Find(ctx, scoped, store.ListOptions{
			Limit: m.service.opts.maxQueryLimit, StartAfter: cursor,
		})
		if err != nil {
			return count, err
		}
		for _, msg := range list.Messages {
			if err := m.service.store.MarkRead(ctx, msg.GetID(), flags.Read != nil && *flags.Read); err == nil {
				count++
			}
		}
		if !list.HasMore || len(list.Messages) == 0 {
			break
		}
		cursor = list.Messages[len(list.Messages)-1].GetID()
	}
	return count, nil
}

func (m *userMailbox) MoveByFilter(ctx context.Context, filters []store.Filter, folderID string) (int64, error) {
	if err := m.checkAccess(); err != nil {
		return 0, err
	}
	scoped := m.scopedFilters(filters)

	if bu, ok := m.service.store.(store.BulkUpdater); ok {
		return bu.MoveByFilter(ctx, m.userID, scoped, folderID)
	}

	var count int64
	var cursor string
	for {
		list, err := m.service.store.Find(ctx, scoped, store.ListOptions{
			Limit: m.service.opts.maxQueryLimit, StartAfter: cursor,
		})
		if err != nil {
			return count, err
		}
		for _, msg := range list.Messages {
			if err := m.service.store.MoveToFolder(ctx, msg.GetID(), folderID); err == nil {
				count++
			}
		}
		if !list.HasMore || len(list.Messages) == 0 {
			break
		}
		cursor = list.Messages[len(list.Messages)-1].GetID()
	}
	return count, nil
}

func (m *userMailbox) DeleteByFilter(ctx context.Context, filters []store.Filter) (int64, error) {
	if err := m.checkAccess(); err != nil {
		return 0, err
	}
	scoped := m.scopedFilters(filters)

	if bu, ok := m.service.store.(store.BulkUpdater); ok {
		return bu.DeleteByFilter(ctx, m.userID, scoped)
	}

	var count int64
	var cursor string
	for {
		list, err := m.service.store.Find(ctx, scoped, store.ListOptions{
			Limit: m.service.opts.maxQueryLimit, StartAfter: cursor,
		})
		if err != nil {
			return count, err
		}
		for _, msg := range list.Messages {
			if err := m.service.store.MoveToFolder(ctx, msg.GetID(), store.FolderTrash); err == nil {
				count++
			}
		}
		if !list.HasMore || len(list.Messages) == 0 {
			break
		}
		cursor = list.Messages[len(list.Messages)-1].GetID()
	}
	return count, nil
}

func (m *userMailbox) TagByFilter(ctx context.Context, filters []store.Filter, tagID string) (int64, error) {
	if err := m.checkAccess(); err != nil {
		return 0, err
	}
	scoped := m.scopedFilters(filters)

	if bu, ok := m.service.store.(store.BulkUpdater); ok {
		return bu.AddTagByFilter(ctx, m.userID, scoped, tagID)
	}

	var count int64
	var cursor string
	for {
		list, err := m.service.store.Find(ctx, scoped, store.ListOptions{
			Limit: m.service.opts.maxQueryLimit, StartAfter: cursor,
		})
		if err != nil {
			return count, err
		}
		for _, msg := range list.Messages {
			if err := m.service.store.AddTag(ctx, msg.GetID(), tagID); err == nil {
				count++
			}
		}
		if !list.HasMore || len(list.Messages) == 0 {
			break
		}
		cursor = list.Messages[len(list.Messages)-1].GetID()
	}
	return count, nil
}

func (m *userMailbox) UntagByFilter(ctx context.Context, filters []store.Filter, tagID string) (int64, error) {
	if err := m.checkAccess(); err != nil {
		return 0, err
	}
	scoped := m.scopedFilters(filters)

	if bu, ok := m.service.store.(store.BulkUpdater); ok {
		return bu.RemoveTagByFilter(ctx, m.userID, scoped, tagID)
	}

	var count int64
	var cursor string
	for {
		list, err := m.service.store.Find(ctx, scoped, store.ListOptions{
			Limit: m.service.opts.maxQueryLimit, StartAfter: cursor,
		})
		if err != nil {
			return count, err
		}
		for _, msg := range list.Messages {
			if err := m.service.store.RemoveTag(ctx, msg.GetID(), tagID); err == nil {
				count++
			}
		}
		if !list.HasMore || len(list.Messages) == 0 {
			break
		}
		cursor = list.Messages[len(list.Messages)-1].GetID()
	}
	return count, nil
}
