package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rbaliyan/mailbox"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestToGRPCError_Sentinels exercises every sentinel branch in toGRPCError so
// that a new sentinel added to mailbox/errors.go without a matching case here
// causes this test to be updated (drift detection via code review).
func TestToGRPCError_Sentinels(t *testing.T) {
	t.Parallel()

	ve := &mailbox.ValidationError{Field: "subject", Message: "too short"}

	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		// nil passthrough
		{"nil", nil, codes.OK, ""},

		// Structured type — checked before sentinels
		{"ValidationError", ve, codes.InvalidArgument, "validation: field=subject too short"},

		// Not found
		{"ErrNotFound", mailbox.ErrNotFound, codes.NotFound, "message not found"},
		{"ErrAttachmentNotFound", mailbox.ErrAttachmentNotFound, codes.NotFound, "attachment not found"},

		// Permission
		{"ErrUnauthorized", mailbox.ErrUnauthorized, codes.PermissionDenied, "access denied"},

		// Conflict / precondition
		{"ErrDuplicateEntry", mailbox.ErrDuplicateEntry, codes.AlreadyExists, "duplicate entry"},
		{"ErrNotInTrash", mailbox.ErrNotInTrash, codes.FailedPrecondition, "message is not in trash"},
		{"ErrAlreadyInTrash", mailbox.ErrAlreadyInTrash, codes.FailedPrecondition, "message is already in trash"},
		{"ErrFolderMismatch", mailbox.ErrFolderMismatch, codes.FailedPrecondition, "message is not in the expected folder"},
		{"ErrAlreadyConnected", mailbox.ErrAlreadyConnected, codes.FailedPrecondition, "already connected"},
		{"ErrUserResolveFailed", mailbox.ErrUserResolveFailed, codes.FailedPrecondition, "user resolution failed"},

		// Invalid argument
		{"ErrInvalidMessage", mailbox.ErrInvalidMessage, codes.InvalidArgument, "invalid message"},
		{"ErrEmptyRecipients", mailbox.ErrEmptyRecipients, codes.InvalidArgument, "at least one recipient is required"},
		{"ErrEmptySubject", mailbox.ErrEmptySubject, codes.InvalidArgument, "subject is required"},
		{"ErrInvalidID", mailbox.ErrInvalidID, codes.InvalidArgument, "invalid message ID"},
		{"ErrInvalidUserID", mailbox.ErrInvalidUserID, codes.InvalidArgument, "invalid user ID"},
		{"ErrFilterInvalid", mailbox.ErrFilterInvalid, codes.InvalidArgument, "invalid filter"},
		{"ErrInvalidMetadata", mailbox.ErrInvalidMetadata, codes.InvalidArgument, "invalid metadata"},
		{"ErrMetadataKeyTooLong", mailbox.ErrMetadataKeyTooLong, codes.InvalidArgument, "metadata key too long"},
		{"ErrMetadataTooLarge", mailbox.ErrMetadataTooLarge, codes.InvalidArgument, "metadata too large"},
		{"ErrSubjectTooLong", mailbox.ErrSubjectTooLong, codes.InvalidArgument, "subject too long"},
		{"ErrBodyTooLarge", mailbox.ErrBodyTooLarge, codes.InvalidArgument, "body too large"},
		{"ErrInvalidContent", mailbox.ErrInvalidContent, codes.InvalidArgument, "invalid content"},
		{"ErrTooManyRecipients", mailbox.ErrTooManyRecipients, codes.InvalidArgument, "too many recipients"},
		{"ErrInvalidRecipient", mailbox.ErrInvalidRecipient, codes.InvalidArgument, "invalid recipient"},
		{"ErrTooManyAttachments", mailbox.ErrTooManyAttachments, codes.InvalidArgument, "too many attachments"},
		{"ErrAttachmentTooLarge", mailbox.ErrAttachmentTooLarge, codes.InvalidArgument, "attachment too large"},
		{"ErrInvalidAttachment", mailbox.ErrInvalidAttachment, codes.InvalidArgument, "invalid attachment"},
		{"ErrInvalidMIMEType", mailbox.ErrInvalidMIMEType, codes.InvalidArgument, "invalid MIME type"},
		{"ErrInvalidHeaders", mailbox.ErrInvalidHeaders, codes.InvalidArgument, "invalid headers"},
		{"ErrTooManyHeaders", mailbox.ErrTooManyHeaders, codes.InvalidArgument, "too many headers"},
		{"ErrHeaderKeyTooLong", mailbox.ErrHeaderKeyTooLong, codes.InvalidArgument, "header key too long"},
		{"ErrHeaderValueTooLong", mailbox.ErrHeaderValueTooLong, codes.InvalidArgument, "header value too long"},
		{"ErrHeadersTooLarge", mailbox.ErrHeadersTooLarge, codes.InvalidArgument, "headers too large"},
		{"ErrInvalidFolderID", mailbox.ErrInvalidFolderID, codes.InvalidArgument, "invalid folder ID"},
		{"ErrInvalidIdempotencyKey", mailbox.ErrInvalidIdempotencyKey, codes.InvalidArgument, "invalid idempotency key"},
		{"ErrInvalidTTL", mailbox.ErrInvalidTTL, codes.InvalidArgument, "invalid TTL"},
		{"ErrInvalidSchedule", mailbox.ErrInvalidSchedule, codes.InvalidArgument, "invalid schedule"},

		// Resource exhausted
		{"ErrRateLimited", mailbox.ErrRateLimited, codes.ResourceExhausted, "rate limited"},
		{"ErrQuotaExceeded", mailbox.ErrQuotaExceeded, codes.ResourceExhausted, "quota exceeded"},

		// Unavailable
		{"ErrNotConnected", mailbox.ErrNotConnected, codes.Unavailable, "service not connected"},

		// Unimplemented
		{"ErrAttachmentStoreNotConfigured", mailbox.ErrAttachmentStoreNotConfigured, codes.Unimplemented, "attachment store not configured"},
		{"ErrNotifierNotConfigured", mailbox.ErrNotifierNotConfigured, codes.Unimplemented, "notifier not configured"},

		// Wrapped sentinel — must still resolve correctly via errors.Is
		{"wrapped ErrNotFound", fmt.Errorf("internal detail: %w", mailbox.ErrNotFound), codes.NotFound, "message not found"},

		// Unknown error — must map to Internal, never forwarding raw message
		{"unknown error", errors.New("database connection refused at /var/db"), codes.Internal, "internal error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := toGRPCError(tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("want nil, got %v", got)
				}
				return
			}
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("toGRPCError did not return a gRPC status error: %v", got)
			}
			if st.Code() != tc.wantCode {
				t.Errorf("code: want %v, got %v", tc.wantCode, st.Code())
			}
			if st.Message() != tc.wantMsg {
				t.Errorf("message: want %q, got %q", tc.wantMsg, st.Message())
			}
		})
	}
}

// TestToGRPCError_NoInternalLeakage verifies that wrapping a sentinel with
// extra context does not cause those details to appear in the gRPC message.
func TestToGRPCError_NoInternalLeakage(t *testing.T) {
	t.Parallel()

	const secret = "secret-internal-detail-xyz"

	sentinels := []error{
		mailbox.ErrNotFound,
		mailbox.ErrUnauthorized,
		mailbox.ErrDuplicateEntry,
		mailbox.ErrBodyTooLarge,
		mailbox.ErrRateLimited,
		mailbox.ErrNotConnected,
	}
	for _, sentinel := range sentinels {
		wrapped := fmt.Errorf("%s: %w", secret, sentinel)
		got := toGRPCError(wrapped)
		st, _ := status.FromError(got)
		if strings.Contains(st.Message(), secret) {
			t.Errorf("toGRPCError leaked internal detail for %v: message=%q", sentinel, st.Message())
		}
	}
}
