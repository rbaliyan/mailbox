package mailbox

import "fmt"

// OperationResult contains the result of a single operation within a bulk operation.
// Results are returned in the same order as the input items.
type OperationResult struct {
	// ID is the identifier of the item that was processed.
	ID string
	// Success indicates whether the operation succeeded.
	Success bool
	// Error contains the error if the operation failed (nil if successful).
	Error error
	// Message contains the sent message (only for DraftList.Send, only if successful).
	Message Message
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

// SentMessages returns all successfully sent messages (for DraftList.Send operations).
func (r *BulkResult) SentMessages() []Message {
	if r == nil {
		return nil
	}
	var msgs []Message
	for _, res := range r.Results {
		if res.Success && res.Message != nil {
			msgs = append(msgs, res.Message)
		}
	}
	return msgs
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
