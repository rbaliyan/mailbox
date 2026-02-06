package mailbox

import (
	"errors"
	"testing"
)

func TestPartialDeliveryError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		err := &PartialDeliveryError{
			MessageID:   "msg123",
			DeliveredTo: []string{"user1", "user2"},
			FailedRecipients: map[string]error{
				"user3": ErrNotFound,
			},
		}

		errMsg := err.Error()
		if errMsg == "" {
			t.Error("expected non-empty error message")
		}

		// Should mention delivered and failed counts
		expectedParts := []string{"2 delivered", "1 failed", "user3"}
		for _, part := range expectedParts {
			if !containsSubstring(errMsg, part) {
				t.Errorf("expected error message to contain %q, got %q", part, errMsg)
			}
		}
	})

	t.Run("Error message with many failures", func(t *testing.T) {
		failed := make(map[string]error)
		for i := 0; i < 10; i++ {
			failed[string(rune('a'+i))] = ErrNotFound
		}

		err := &PartialDeliveryError{
			MessageID:        "msg123",
			DeliveredTo:      []string{"user1"},
			FailedRecipients: failed,
		}

		errMsg := err.Error()
		// Should truncate after 5 failures
		if !containsSubstring(errMsg, "more") {
			t.Errorf("expected error message to truncate with '...and X more', got %q", errMsg)
		}
	})

	t.Run("Unwrap returns ErrPartialDelivery", func(t *testing.T) {
		err := &PartialDeliveryError{
			MessageID:        "msg123",
			DeliveredTo:      []string{"user1"},
			FailedRecipients: map[string]error{"user2": ErrNotFound},
		}

		if !errors.Is(err, ErrPartialDelivery) {
			t.Error("expected errors.Is to return true for ErrPartialDelivery")
		}
	})
}

func TestPartialDeliveryErrorRetryGuidance(t *testing.T) {
	t.Run("RetryableRecipients", func(t *testing.T) {
		err := &PartialDeliveryError{
			MessageID:   "msg123",
			DeliveredTo: []string{"user1"},
			FailedRecipients: map[string]error{
				"user2": ErrNotFound,      // permanent
				"user3": ErrRateLimited,   // retryable
				"user4": ErrNotConnected,  // retryable
				"user5": ErrInvalidUserID, // permanent
			},
		}

		retryable := err.RetryableRecipients()
		if len(retryable) != 2 {
			t.Errorf("expected 2 retryable recipients, got %d", len(retryable))
		}
	})

	t.Run("PermanentFailures", func(t *testing.T) {
		err := &PartialDeliveryError{
			MessageID:   "msg123",
			DeliveredTo: []string{"user1"},
			FailedRecipients: map[string]error{
				"user2": ErrNotFound,    // permanent
				"user3": ErrRateLimited, // retryable
			},
		}

		permanent := err.PermanentFailures()
		if len(permanent) != 1 {
			t.Errorf("expected 1 permanent failure, got %d", len(permanent))
		}
		if permanent[0] != "user2" {
			t.Errorf("expected user2, got %s", permanent[0])
		}
	})

	t.Run("HasRetryableFailures", func(t *testing.T) {
		errWithRetryable := &PartialDeliveryError{
			FailedRecipients: map[string]error{
				"user1": ErrRateLimited,
			},
		}
		if !errWithRetryable.HasRetryableFailures() {
			t.Error("expected HasRetryableFailures to return true")
		}

		errNoneRetryable := &PartialDeliveryError{
			FailedRecipients: map[string]error{
				"user1": ErrNotFound,
			},
		}
		if errNoneRetryable.HasRetryableFailures() {
			t.Error("expected HasRetryableFailures to return false")
		}
	})

	t.Run("AllFailed", func(t *testing.T) {
		errAllFailed := &PartialDeliveryError{
			DeliveredTo: []string{},
			FailedRecipients: map[string]error{
				"user1": ErrNotFound,
			},
		}
		if !errAllFailed.AllFailed() {
			t.Error("expected AllFailed to return true")
		}

		errPartial := &PartialDeliveryError{
			DeliveredTo: []string{"user1"},
			FailedRecipients: map[string]error{
				"user2": ErrNotFound,
			},
		}
		if errPartial.AllFailed() {
			t.Error("expected AllFailed to return false")
		}
	})

	t.Run("SuccessRate", func(t *testing.T) {
		err := &PartialDeliveryError{
			DeliveredTo: []string{"user1", "user2"},
			FailedRecipients: map[string]error{
				"user3": ErrNotFound,
				"user4": ErrNotFound,
			},
		}
		rate := err.SuccessRate()
		if rate != 0.5 {
			t.Errorf("expected success rate 0.5, got %f", rate)
		}

		emptyErr := &PartialDeliveryError{}
		if emptyErr.SuccessRate() != 0 {
			t.Error("expected 0 success rate for empty error")
		}
	})
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    bool
	}{
		{"nil error", nil, false},
		{"ErrNotFound is permanent", ErrNotFound, false},
		{"ErrUnauthorized is permanent", ErrUnauthorized, false},
		{"ErrInvalidMessage is permanent", ErrInvalidMessage, false},
		{"ErrInvalidRecipient is permanent", ErrInvalidRecipient, false},
		{"ErrDuplicateEntry is permanent", ErrDuplicateEntry, false},
		{"ErrRateLimited is retryable", ErrRateLimited, true},
		{"ErrNotConnected is retryable", ErrNotConnected, true},
		{"ErrCacheInvalidationFailed is retryable", ErrCacheInvalidationFailed, true},
		{"unknown error is retryable", errors.New("some unknown error"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableError(tt.err); got != tt.want {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsPartialDelivery(t *testing.T) {
	t.Run("returns true for PartialDeliveryError", func(t *testing.T) {
		err := &PartialDeliveryError{
			MessageID:        "msg123",
			DeliveredTo:      []string{"user1"},
			FailedRecipients: map[string]error{"user2": ErrNotFound},
		}

		pde, ok := IsPartialDelivery(err)
		if !ok {
			t.Fatal("expected IsPartialDelivery to return true")
		}
		if pde == nil {
			t.Fatal("expected non-nil PartialDeliveryError")
		}
		if pde.MessageID != "msg123" {
			t.Errorf("expected MessageID 'msg123', got %q", pde.MessageID)
		}
	})

	t.Run("returns true for wrapped PartialDeliveryError", func(t *testing.T) {
		innerErr := &PartialDeliveryError{
			MessageID:        "msg456",
			DeliveredTo:      []string{"user1", "user2"},
			FailedRecipients: map[string]error{"user3": ErrNotFound},
		}
		wrappedErr := &wrappedError{err: innerErr}

		pde, ok := IsPartialDelivery(wrappedErr)
		if !ok {
			t.Error("expected IsPartialDelivery to return true for wrapped error")
		}
		if pde == nil {
			t.Error("expected non-nil PartialDeliveryError")
		}
	})

	t.Run("returns false for other errors", func(t *testing.T) {
		pde, ok := IsPartialDelivery(ErrNotFound)
		if ok {
			t.Error("expected IsPartialDelivery to return false for non-PartialDeliveryError")
		}
		if pde != nil {
			t.Error("expected nil PartialDeliveryError")
		}
	})

	t.Run("returns false for nil error", func(t *testing.T) {
		pde, ok := IsPartialDelivery(nil)
		if ok {
			t.Error("expected IsPartialDelivery to return false for nil")
		}
		if pde != nil {
			t.Error("expected nil PartialDeliveryError")
		}
	})
}

func TestSentinelErrors(t *testing.T) {
	// Test that all sentinel errors are distinct
	sentinelErrors := []error{
		ErrNotFound,
		ErrUnauthorized,
		ErrInvalidMessage,
		ErrEmptyRecipients,
		ErrEmptySubject,
		ErrStoreRequired,
		ErrNotConnected,
		ErrAlreadyConnected,
		ErrInvalidID,
		ErrDuplicateEntry,
		ErrFilterInvalid,
		ErrEventClientRequired,
		ErrNotInTrash,
		ErrAlreadyInTrash,
		ErrAttachmentNotFound,
		ErrAttachmentStoreNotConfigured,
		ErrPartialDelivery,
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
		ErrInvalidMIMEType,
		ErrInvalidFolderID,
		ErrRateLimited,
		ErrInvalidUserID,
		ErrInvalidIdempotencyKey,
		ErrCacheInvalidationFailed,
	}

	// Check that each error has a non-empty message
	for i, err := range sentinelErrors {
		if err.Error() == "" {
			t.Errorf("sentinel error at index %d has empty message", i)
		}
	}

	// Check that all errors are distinct
	seen := make(map[string]int)
	for i, err := range sentinelErrors {
		msg := err.Error()
		if prevIndex, exists := seen[msg]; exists {
			t.Errorf("duplicate error message %q at indices %d and %d", msg, prevIndex, i)
		}
		seen[msg] = i
	}
}

func TestErrorsIs(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"ErrNotFound matches itself", ErrNotFound, ErrNotFound, true},
		{"ErrNotFound doesn't match ErrUnauthorized", ErrNotFound, ErrUnauthorized, false},
		{"wrapped error matches", wrappedError{err: ErrRateLimited}, ErrRateLimited, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, tt.target); got != tt.want {
				t.Errorf("errors.Is(%v, %v) = %v, want %v", tt.err, tt.target, got, tt.want)
			}
		})
	}
}

// wrappedError is a helper for testing error wrapping
type wrappedError struct {
	err error
}

func (e wrappedError) Error() string { return "wrapped: " + e.err.Error() }
func (e wrappedError) Unwrap() error { return e.err }

// containsSubstring checks if s contains substr
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
