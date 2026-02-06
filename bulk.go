package mailbox

import (
	"context"
	"fmt"
)

// OperationResult contains the result of a single operation within a bulk operation.
// Results are returned in the same order as the input items.
type OperationResult struct {
	// ID is the identifier of the item that was processed.
	ID string
	// Success indicates whether the operation succeeded.
	Success bool
	// Error contains the error if the operation failed (nil if successful).
	Error error
}

// BulkResult contains the result of a bulk operation.
// Used consistently across MessageList and DraftList operations.
//
// Results are returned in order, matching the input order.
// Use helper methods to check status and iterate results.
type BulkResult struct {
	// Results contains the outcome of each operation in input order.
	Results []OperationResult
}

// SuccessCount returns the number of successful operations.
func (r *BulkResult) SuccessCount() int {
	if r == nil {
		return 0
	}
	count := 0
	for _, res := range r.Results {
		if res.Success {
			count++
		}
	}
	return count
}

// FailureCount returns the number of failed operations.
func (r *BulkResult) FailureCount() int {
	if r == nil {
		return 0
	}
	count := 0
	for _, res := range r.Results {
		if !res.Success {
			count++
		}
	}
	return count
}

// HasFailures returns true if any operations failed.
func (r *BulkResult) HasFailures() bool {
	if r == nil {
		return false
	}
	for _, res := range r.Results {
		if !res.Success {
			return true
		}
	}
	return false
}

// TotalCount returns the total number of items processed.
func (r *BulkResult) TotalCount() int {
	if r == nil {
		return 0
	}
	return len(r.Results)
}

// FailedIDs returns the IDs of items that failed.
func (r *BulkResult) FailedIDs() []string {
	if r == nil {
		return nil
	}
	var ids []string
	for _, res := range r.Results {
		if !res.Success {
			ids = append(ids, res.ID)
		}
	}
	return ids
}

// SuccessfulIDs returns the IDs of items that succeeded.
func (r *BulkResult) SuccessfulIDs() []string {
	if r == nil {
		return nil
	}
	var ids []string
	for _, res := range r.Results {
		if res.Success {
			ids = append(ids, res.ID)
		}
	}
	return ids
}

// Err returns an error if there are failures, nil otherwise.
func (r *BulkResult) Err() error {
	if r == nil {
		return nil
	}
	if !r.HasFailures() {
		return nil
	}
	return &BulkOperationError{Result: r}
}

// BulkOperationError is returned when a bulk operation has partial failures.
// It wraps BulkResult to provide error interface while guaranteeing non-empty Error().
type BulkOperationError struct {
	Result *BulkResult
}

// Error implements the error interface.
// Always returns a non-empty string describing the failure.
func (e *BulkOperationError) Error() string {
	return fmt.Sprintf("mailbox: bulk operation failed for %d of %d items",
		e.Result.FailureCount(), e.Result.TotalCount())
}

// Unwrap returns the individual errors from failed operations.
func (e *BulkOperationError) Unwrap() []error {
	var errs []error
	for _, r := range e.Result.Results {
		if r.Error != nil {
			errs = append(errs, r.Error)
		}
	}
	return errs
}

// DraftSendResult extends BulkResult with sent messages from DraftList.Send().
type DraftSendResult struct {
	*BulkResult
	sentMessages []Message
}

// SentMessages returns all successfully sent messages in order.
func (r *DraftSendResult) SentMessages() []Message {
	if r == nil {
		return nil
	}
	return r.sentMessages
}

// --- Bulk operations on userMailbox ---

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
