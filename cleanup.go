package mailbox

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// CleanupTrashResult contains the result of a trash cleanup operation.
type CleanupTrashResult struct {
	// DeletedCount is the number of messages permanently deleted.
	DeletedCount int
	// Interrupted indicates if the cleanup was interrupted (e.g., context cancelled).
	Interrupted bool
}

// CleanupTrash permanently deletes messages that have been in trash longer than
// the configured retention period (default 30 days).
//
// This method processes all expired trash messages in batches until complete
// or the context is cancelled. It uses the store's atomic DeleteExpiredTrash
// operation for efficient bulk deletion.
//
// This method should be called periodically by the application using its own
// scheduler (e.g., cron job, background worker). The library does not
// automatically run cleanup to give applications full control over scheduling.
//
// Example with a simple ticker:
//
//	go func() {
//	    ticker := time.NewTicker(1 * time.Hour)
//	    defer ticker.Stop()
//	    for range ticker.C {
//	        result, err := svc.CleanupTrash(ctx)
//	        if err != nil {
//	            log.Printf("trash cleanup error: %v", err)
//	        } else if result.DeletedCount > 0 {
//	            log.Printf("cleaned up %d expired trash messages", result.DeletedCount)
//	        }
//	    }
//	}()
func (s *service) CleanupTrash(ctx context.Context) (*CleanupTrashResult, error) {
	if atomic.LoadInt32(&s.state) != stateConnected {
		return nil, ErrNotConnected
	}

	result := &CleanupTrashResult{}
	cutoff := time.Now().UTC().Add(-s.opts.trashRetention)

	if s.attachments != nil {
		return s.cleanupTrashWithAttachments(ctx, result, cutoff)
	}

	// Fast path: no attachments, bulk delete by cutoff.
	deleted, err := s.store.DeleteExpiredTrash(ctx, cutoff)
	if err != nil {
		return result, fmt.Errorf("delete expired trash: %w", err)
	}
	result.DeletedCount = int(deleted)
	if deleted > 0 {
		s.logger.Debug("deleted expired trash messages", "count", deleted)
	}

	return result, nil
}

// cleanupTrashWithAttachments handles trash cleanup when attachments are configured.
// It scans expired messages to collect attachment refs, then uses DeleteMessagesByIDs
// to atomically determine which messages this instance deleted. Only the winning
// instance releases attachment refs, preventing double-decrements in multi-instance
// deployments.
func (s *service) cleanupTrashWithAttachments(ctx context.Context, result *CleanupTrashResult, cutoff time.Time) (*CleanupTrashResult, error) {
	// Step 1: Scan expired trash messages and collect their attachment IDs.
	messageAttachments, scannedIDs, err := s.scanExpiredMessages(ctx, result, []store.Filter{
		store.InFolder(store.FolderTrash),
	}, "UpdatedAt", cutoff)
	if err != nil {
		return result, err
	}

	// Step 2: Delete only the scanned messages by ID. The store returns which IDs
	// this instance actually deleted (the "winners"). This prevents both:
	// - Deleting messages we didn't scan (no leaked attachment refs)
	// - Double-decrementing refs in multi-instance deployments
	deletedIDs, err := s.store.DeleteMessagesByIDs(ctx, scannedIDs)
	if err != nil {
		return result, fmt.Errorf("delete expired trash by IDs: %w", err)
	}
	result.DeletedCount = len(deletedIDs)
	if len(deletedIDs) > 0 {
		s.logger.Debug("deleted expired trash messages", "count", len(deletedIDs))
	}

	// Step 3: Release attachment refs only for messages this instance deleted.
	s.releaseAttachmentRefs(ctx, deletedIDs, messageAttachments)

	return result, nil
}

// CleanupExpiredMessagesResult contains the result of a message retention cleanup.
type CleanupExpiredMessagesResult struct {
	// DeletedCount is the number of messages permanently deleted.
	DeletedCount int
	// Interrupted indicates if the cleanup was interrupted (e.g., context cancelled).
	Interrupted bool
}

// CleanupExpiredMessages permanently deletes messages older than the configured
// message retention period (based on created_at). Returns a zero result if
// message retention is not configured (WithMessageRetention was not called).
//
// This method should be called periodically by the application using its own
// scheduler. The library does not automatically run cleanup.
func (s *service) CleanupExpiredMessages(ctx context.Context) (*CleanupExpiredMessagesResult, error) {
	if atomic.LoadInt32(&s.state) != stateConnected {
		return nil, ErrNotConnected
	}

	result := &CleanupExpiredMessagesResult{}

	// Global retention cleanup (based on created_at).
	if s.opts.messageRetention > 0 {
		cutoff := time.Now().UTC().Add(-s.opts.messageRetention)

		if s.attachments != nil {
			retentionResult, err := s.cleanupExpiredWithAttachments(ctx, &CleanupExpiredMessagesResult{}, cutoff)
			if err != nil {
				return result, err
			}
			result.DeletedCount += retentionResult.DeletedCount
			result.Interrupted = retentionResult.Interrupted
		} else {
			deleted, err := s.store.DeleteExpiredMessages(ctx, cutoff)
			if err != nil {
				return result, fmt.Errorf("delete expired messages: %w", err)
			}
			result.DeletedCount += int(deleted)
			if deleted > 0 {
				s.logger.Debug("deleted expired messages", "count", deleted)
			}
		}
	}

	// Per-message TTL cleanup (based on expires_at).
	if ctx.Err() != nil {
		result.Interrupted = true
		return result, ctx.Err()
	}

	if s.attachments != nil {
		ttlResult, err := s.cleanupTTLExpiredWithAttachments(ctx)
		if err != nil {
			return result, err
		}
		result.DeletedCount += ttlResult
	} else {
		ttlDeleted, err := s.store.DeleteTTLExpiredMessages(ctx, time.Now().UTC())
		if err != nil {
			return result, fmt.Errorf("delete TTL expired messages: %w", err)
		}
		result.DeletedCount += int(ttlDeleted)
		if ttlDeleted > 0 {
			s.logger.Debug("deleted TTL expired messages", "count", ttlDeleted)
		}
	}

	return result, nil
}

// cleanupTTLExpiredWithAttachments handles per-message TTL cleanup when
// attachments are configured. Same safe pattern as cleanupTrashWithAttachments.
func (s *service) cleanupTTLExpiredWithAttachments(ctx context.Context) (int, error) {
	now := time.Now().UTC()

	// The exists filter ensures we only scan messages that have expires_at set.
	// scanExpiredMessages will add the ExpiresAt < now condition.
	existsFilter, err := store.MessageFilter("ExpiresAt").Exists(true)
	if err != nil {
		return 0, fmt.Errorf("create expires_at exists filter: %w", err)
	}

	dummyResult := &CleanupExpiredMessagesResult{}
	messageAttachments, scannedIDs, err := s.scanExpiredMessages(ctx, dummyResult, []store.Filter{
		existsFilter,
	}, "ExpiresAt", now)
	if err != nil {
		return 0, err
	}

	if len(scannedIDs) == 0 {
		return 0, nil
	}

	deletedIDs, err := s.store.DeleteMessagesByIDs(ctx, scannedIDs)
	if err != nil {
		return 0, fmt.Errorf("delete TTL expired messages by IDs: %w", err)
	}
	if len(deletedIDs) > 0 {
		s.logger.Debug("deleted TTL expired messages", "count", len(deletedIDs))
	}

	s.releaseAttachmentRefs(ctx, deletedIDs, messageAttachments)

	return len(deletedIDs), nil
}

// cleanupExpiredWithAttachments handles message retention cleanup when attachments
// are configured. Same safe pattern as cleanupTrashWithAttachments.
func (s *service) cleanupExpiredWithAttachments(ctx context.Context, result *CleanupExpiredMessagesResult, cutoff time.Time) (*CleanupExpiredMessagesResult, error) {
	// Step 1: Scan expired messages and collect their attachment IDs.
	messageAttachments, scannedIDs, err := s.scanExpiredMessages(ctx, result, []store.Filter{
		store.IsDraftFilter(false),
	}, "CreatedAt", cutoff)
	if err != nil {
		return result, err
	}

	// Step 2: Delete only the scanned messages by ID.
	deletedIDs, err := s.store.DeleteMessagesByIDs(ctx, scannedIDs)
	if err != nil {
		return result, fmt.Errorf("delete expired messages by IDs: %w", err)
	}
	result.DeletedCount = len(deletedIDs)
	if len(deletedIDs) > 0 {
		s.logger.Debug("deleted expired messages", "count", len(deletedIDs))
	}

	// Step 3: Release attachment refs only for messages this instance deleted.
	s.releaseAttachmentRefs(ctx, deletedIDs, messageAttachments)

	return result, nil
}

// interruptible is implemented by result types that track interruption.
type interruptible interface {
	setInterrupted()
}

func (r *CleanupTrashResult) setInterrupted()           { r.Interrupted = true }
func (r *CleanupExpiredMessagesResult) setInterrupted()  { r.Interrupted = true }

// scanExpiredMessages scans messages matching the given filters and time cutoff,
// collecting all message IDs and their attachment IDs. Returns the attachment map
// and the list of all scanned message IDs.
func (s *service) scanExpiredMessages(ctx context.Context, result interruptible, baseFilters []store.Filter, timeField string, cutoff time.Time) (map[string][]string, []string, error) {
	timeFilter, err := store.MessageFilter(timeField).LessThan(cutoff)
	if err != nil {
		return nil, nil, fmt.Errorf("create time filter: %w", err)
	}
	filters := make([]store.Filter, len(baseFilters)+1)
	copy(filters, baseFilters)
	filters[len(baseFilters)] = timeFilter

	messageAttachments := make(map[string][]string)
	var scannedIDs []string

	const scanBatchSize = 100
	var cursor string
	for {
		if ctx.Err() != nil {
			result.setInterrupted()
			return messageAttachments, scannedIDs, ctx.Err()
		}

		opts := store.ListOptions{Limit: scanBatchSize, StartAfter: cursor}
		list, err := s.store.Find(ctx, filters, opts)
		if err != nil {
			return messageAttachments, scannedIDs, fmt.Errorf("find expired messages: %w", err)
		}

		for _, msg := range list.Messages {
			scannedIDs = append(scannedIDs, msg.GetID())
			var ids []string
			for _, a := range msg.GetAttachments() {
				ids = append(ids, a.GetID())
			}
			if len(ids) > 0 {
				messageAttachments[msg.GetID()] = ids
			}
		}

		if !list.HasMore || len(list.Messages) == 0 {
			break
		}
		cursor = list.Messages[len(list.Messages)-1].GetID()
	}

	return messageAttachments, scannedIDs, nil
}

// releaseAttachmentRefs releases attachment references for messages that were
// confirmed deleted by this instance. Only call this with IDs returned by
// DeleteMessagesByIDs to prevent double-decrements across instances.
func (s *service) releaseAttachmentRefs(ctx context.Context, deletedIDs []string, messageAttachments map[string][]string) {
	for _, msgID := range deletedIDs {
		attIDs, ok := messageAttachments[msgID]
		if !ok {
			continue
		}
		for _, attachmentID := range attIDs {
			if err := s.attachments.RemoveRef(ctx, attachmentID); err != nil {
				s.logger.Warn("failed to release attachment ref during cleanup",
					"error", err, "attachment_id", attachmentID, "message_id", msgID)
			}
		}
	}
}
