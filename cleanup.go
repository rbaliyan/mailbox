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

	// Step 1: Collect message IDs and their attachment IDs from expired trash.
	// This must happen before deletion so we know which attachment refs to release.
	// We track per-message attachment IDs so we can verify each message was actually
	// deleted before releasing its refs (prevents incorrect ref decrements if a
	// message is restored between the scan and delete steps).
	messageAttachments := make(map[string][]string) // messageID -> []attachmentID
	if s.attachments != nil {
		updatedBeforeFilter, err := store.MessageFilter("UpdatedAt").LessThan(cutoff)
		if err != nil {
			return result, fmt.Errorf("create trash filter: %w", err)
		}
		filters := []store.Filter{
			store.InFolder(store.FolderTrash),
			updatedBeforeFilter,
		}

		const scanBatchSize = 100
		var cursor string
		for {
			if ctx.Err() != nil {
				result.Interrupted = true
				return result, ctx.Err()
			}

			opts := store.ListOptions{Limit: scanBatchSize, StartAfter: cursor}
			list, err := s.store.Find(ctx, filters, opts)
			if err != nil {
				return result, fmt.Errorf("find expired trash: %w", err)
			}

			for _, msg := range list.Messages {
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
	}

	// Step 2: Bulk delete all expired trash messages atomically.
	deleted, err := s.store.DeleteExpiredTrash(ctx, cutoff)
	if err != nil {
		return result, fmt.Errorf("delete expired trash: %w", err)
	}
	result.DeletedCount = int(deleted)
	if deleted > 0 {
		s.logger.Debug("deleted expired trash messages", "count", deleted)
	}

	// Step 3: Release attachment references only for messages confirmed deleted.
	// A message may have been restored between step 1 and step 2. Verify each
	// message no longer exists before releasing its attachment refs to prevent
	// incorrect ref decrements that could cause premature attachment deletion.
	//
	// TOCTOU note: There is a small window between Get-check and RemoveRef where
	// a message could theoretically be re-created with the same attachments, leading
	// to a double-decrement. In practice this is extremely unlikely (requires exact
	// same message ID reuse) and the worst case is an orphaned attachment file that
	// gets cleaned up in the next cycle. True atomic "delete message + release refs"
	// would require a store-level transaction spanning both stores.
	if s.attachments != nil {
		for msgID, attIDs := range messageAttachments {
			// Verify message was actually deleted (not restored between scan and delete).
			if _, getErr := s.store.Get(ctx, msgID); getErr == nil {
				// Message still exists (was restored) - skip releasing its refs.
				s.logger.Debug("skipping attachment ref release for restored message",
					"message_id", msgID)
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

	return result, nil
}
