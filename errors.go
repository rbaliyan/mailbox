package mailbox

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors for the mailbox package.
// Use errors.Is() to check for these errors.
var (
	// ErrNotFound is returned when a message cannot be found.
	ErrNotFound = errors.New("mailbox: not found")

	// ErrUnauthorized is returned when user doesn't have access to a message.
	ErrUnauthorized = errors.New("mailbox: unauthorized")

	// ErrInvalidMessage is returned for message validation failures.
	ErrInvalidMessage = errors.New("mailbox: invalid message")

	// ErrEmptyRecipients is returned when no recipients are provided.
	ErrEmptyRecipients = errors.New("mailbox: empty recipients")

	// ErrEmptySubject is returned when subject is empty.
	ErrEmptySubject = errors.New("mailbox: empty subject")

	// ErrStoreRequired is returned when no store is configured.
	ErrStoreRequired = errors.New("mailbox: store is required")

	// ErrNotConnected is returned when operations are attempted before Connect().
	ErrNotConnected = errors.New("mailbox: not connected")

	// ErrAlreadyConnected is returned when Connect() is called twice.
	ErrAlreadyConnected = errors.New("mailbox: already connected")

	// ErrInvalidID is returned when an invalid ID is provided.
	ErrInvalidID = errors.New("mailbox: invalid id")

	// ErrDuplicateEntry is returned when a duplicate entry is detected.
	ErrDuplicateEntry = errors.New("mailbox: duplicate entry")

	// ErrFilterInvalid is returned when a filter is invalid.
	ErrFilterInvalid = errors.New("mailbox: invalid filter")

	// ErrEventClientRequired is returned when event client is nil.
	ErrEventClientRequired = errors.New("mailbox: event client is required")

	// ErrNotInTrash is returned when trying to restore a message not in trash.
	ErrNotInTrash = errors.New("mailbox: message not in trash")

	// ErrAlreadyInTrash is returned when trying to trash an already trashed message.
	ErrAlreadyInTrash = errors.New("mailbox: message already in trash")

	// ErrAttachmentNotFound is returned when an attachment cannot be found.
	ErrAttachmentNotFound = errors.New("mailbox: attachment not found")

	// ErrAttachmentStoreNotConfigured is returned when attachment store is not configured.
	ErrAttachmentStoreNotConfigured = errors.New("mailbox: attachment store not configured")

	// ErrPartialDelivery is returned when some recipients failed to receive the message.
	ErrPartialDelivery = errors.New("mailbox: partial delivery")

	// ErrInvalidMetadata is returned when metadata validation fails.
	ErrInvalidMetadata = errors.New("mailbox: invalid metadata")

	// ErrMetadataKeyTooLong is returned when a metadata key exceeds the maximum length.
	ErrMetadataKeyTooLong = errors.New("mailbox: metadata key too long")

	// ErrMetadataTooLarge is returned when metadata exceeds the maximum size.
	ErrMetadataTooLarge = errors.New("mailbox: metadata too large")

	// ErrSubjectTooLong is returned when subject exceeds maximum length.
	ErrSubjectTooLong = errors.New("mailbox: subject too long")

	// ErrBodyTooLarge is returned when body exceeds maximum size.
	ErrBodyTooLarge = errors.New("mailbox: body too large")

	// ErrInvalidContent is returned when message content contains invalid characters.
	ErrInvalidContent = errors.New("mailbox: invalid content")

	// ErrTooManyRecipients is returned when recipient count exceeds the limit.
	ErrTooManyRecipients = errors.New("mailbox: too many recipients")

	// ErrInvalidRecipient is returned when a recipient ID is invalid.
	ErrInvalidRecipient = errors.New("mailbox: invalid recipient")

	// ErrTooManyAttachments is returned when attachment count exceeds the limit.
	ErrTooManyAttachments = errors.New("mailbox: too many attachments")

	// ErrAttachmentTooLarge is returned when an attachment exceeds the size limit.
	ErrAttachmentTooLarge = errors.New("mailbox: attachment too large")

	// ErrInvalidAttachment is returned when attachment data is invalid.
	ErrInvalidAttachment = errors.New("mailbox: invalid attachment")

	// ErrInvalidMIMEType is returned when an attachment has an invalid or disallowed MIME type.
	ErrInvalidMIMEType = errors.New("mailbox: invalid mime type")

	// ErrInvalidFolderID is returned when a folder ID is invalid.
	ErrInvalidFolderID = errors.New("mailbox: invalid folder id")

	// ErrRateLimited is returned when a user exceeds their rate limit.
	ErrRateLimited = errors.New("mailbox: rate limited")

	// ErrInvalidUserID is returned when a user ID contains invalid characters.
	ErrInvalidUserID = errors.New("mailbox: invalid user id")

	// ErrInvalidIdempotencyKey is returned when an idempotency key is invalid.
	ErrInvalidIdempotencyKey = errors.New("mailbox: invalid idempotency key")

	// ErrCacheInvalidationFailed is returned when cache invalidation fails in strict mode.
	// The underlying operation succeeded, but cached data may be stale.
	ErrCacheInvalidationFailed = errors.New("mailbox: cache invalidation failed")
)

// PartialDeliveryError provides details about which recipients failed.
// Use the helper methods to determine retry strategy.
type PartialDeliveryError struct {
	// MessageID is the sender's message ID.
	MessageID string
	// DeliveredTo contains recipient IDs that received the message.
	DeliveredTo []string
	// FailedRecipients maps recipient IDs to their delivery errors.
	FailedRecipients map[string]error
}

func (e *PartialDeliveryError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "mailbox: partial delivery - %d delivered, %d failed",
		len(e.DeliveredTo), len(e.FailedRecipients))
	if len(e.FailedRecipients) > 0 {
		sb.WriteString(" (failed: ")
		count := 0
		const maxShown = 5
		for id := range e.FailedRecipients {
			if count > 0 {
				sb.WriteString(", ")
			}
			if count >= maxShown {
				fmt.Fprintf(&sb, "...and %d more", len(e.FailedRecipients)-maxShown)
				break
			}
			sb.WriteString(id)
			count++
		}
		sb.WriteString(")")
	}
	return sb.String()
}

func (e *PartialDeliveryError) Unwrap() error {
	return ErrPartialDelivery
}

// RetryableRecipients returns the list of recipient IDs whose delivery can be retried.
// An error is considered retryable if it's a temporary/transient failure (e.g., timeout,
// connection error) rather than a permanent failure (e.g., recipient not found, unauthorized).
func (e *PartialDeliveryError) RetryableRecipients() []string {
	retryable := make([]string, 0, len(e.FailedRecipients))
	for recipientID, err := range e.FailedRecipients {
		if IsRetryableError(err) {
			retryable = append(retryable, recipientID)
		}
	}
	return retryable
}

// PermanentFailures returns the list of recipient IDs with permanent failures.
// These recipients should not be retried as the error is deterministic.
func (e *PartialDeliveryError) PermanentFailures() []string {
	permanent := make([]string, 0, len(e.FailedRecipients))
	for recipientID, err := range e.FailedRecipients {
		if !IsRetryableError(err) {
			permanent = append(permanent, recipientID)
		}
	}
	return permanent
}

// HasRetryableFailures returns true if at least one failure can be retried.
func (e *PartialDeliveryError) HasRetryableFailures() bool {
	for _, err := range e.FailedRecipients {
		if IsRetryableError(err) {
			return true
		}
	}
	return false
}

// AllFailed returns true if no recipients received the message.
func (e *PartialDeliveryError) AllFailed() bool {
	return len(e.DeliveredTo) == 0
}

// SuccessRate returns the fraction of recipients that received the message (0.0 to 1.0).
func (e *PartialDeliveryError) SuccessRate() float64 {
	total := len(e.DeliveredTo) + len(e.FailedRecipients)
	if total == 0 {
		return 0
	}
	return float64(len(e.DeliveredTo)) / float64(total)
}

// IsRetryableError determines if an error is retryable.
// Returns true for temporary/transient errors, false for permanent errors.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// Permanent errors that should not be retried
	permanentErrors := []error{
		ErrNotFound,
		ErrUnauthorized,
		ErrInvalidMessage,
		ErrEmptyRecipients,
		ErrEmptySubject,
		ErrInvalidID,
		ErrInvalidMetadata,
		ErrMetadataKeyTooLong,
		ErrMetadataTooLarge,
		ErrSubjectTooLong,
		ErrBodyTooLarge,
		ErrInvalidContent,
		ErrTooManyRecipients,
		ErrInvalidRecipient,
		ErrTooManyAttachments,
		ErrAttachmentTooLarge,
		ErrInvalidAttachment,
		ErrInvalidFolderID,
		ErrInvalidUserID,
		ErrInvalidIdempotencyKey,
		ErrDuplicateEntry,
	}

	for _, permErr := range permanentErrors {
		if errors.Is(err, permErr) {
			return false
		}
	}

	// Retryable errors
	retryableErrors := []error{
		ErrRateLimited,          // Rate limit can be waited out
		ErrNotConnected,         // Connection can be re-established
		ErrCacheInvalidationFailed, // Cache issues are transient
	}

	for _, retryErr := range retryableErrors {
		if errors.Is(err, retryErr) {
			return true
		}
	}

	// For unknown errors, default to retryable (conservative approach)
	// as they might be transient network/timeout issues
	return true
}

// IsPartialDelivery checks if the error is a partial delivery error and returns details.
func IsPartialDelivery(err error) (*PartialDeliveryError, bool) {
	var pde *PartialDeliveryError
	if errors.As(err, &pde) {
		return pde, true
	}
	return nil, false
}

// ValidationError provides details about a validation failure.
type ValidationError struct {
	Field   string // The field that failed validation
	Message string // Human-readable error message
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("mailbox: validation failed for %s: %s", e.Field, e.Message)
}

func (e *ValidationError) Unwrap() error {
	return ErrInvalidMessage
}

// EventPublishError is returned when event publishing fails but the operation succeeded.
// The message was sent/read/deleted, but the event notification failed.
// Check the MessageID field to identify which message this applies to.
type EventPublishError struct {
	Event     string // The event name (e.g., "MessageSent", "MessageRead")
	MessageID string // The message ID the event was for
	Err       error  // The underlying publish error
}

func (e *EventPublishError) Error() string {
	return fmt.Sprintf("mailbox: event %s publish failed for message %s: %v", e.Event, e.MessageID, e.Err)
}

func (e *EventPublishError) Unwrap() error {
	return e.Err
}

// IsEventPublishError checks if the error is an event publish error and returns details.
// This is useful when eventErrorsFatal=true but you still want to know the message was sent.
func IsEventPublishError(err error) (*EventPublishError, bool) {
	var epe *EventPublishError
	if errors.As(err, &epe) {
		return epe, true
	}
	return nil, false
}

// AttachmentRefError provides details about attachment reference counting failures.
// This error is returned when adding or releasing attachment references fails.
// Use errors.As() to extract and inspect the details.
type AttachmentRefError struct {
	// Operation is either "add" or "release"
	Operation string
	// Failed maps attachment IDs to their errors
	Failed map[string]error
	// RollbackFailed maps attachment IDs to rollback errors (only for "add" operations)
	RollbackFailed map[string]error
}

func (e *AttachmentRefError) Error() string {
	msg := fmt.Sprintf("mailbox: %s attachment refs failed for %d attachments", e.Operation, len(e.Failed))
	if len(e.RollbackFailed) > 0 {
		msg += fmt.Sprintf(" (rollback also failed for %d attachments)", len(e.RollbackFailed))
	}
	return msg
}

// HasRollbackFailures returns true if rollback also failed during an add operation.
func (e *AttachmentRefError) HasRollbackFailures() bool {
	return len(e.RollbackFailed) > 0
}

// FailedIDs returns the list of attachment IDs that failed.
func (e *AttachmentRefError) FailedIDs() []string {
	ids := make([]string, 0, len(e.Failed))
	for id := range e.Failed {
		ids = append(ids, id)
	}
	return ids
}
