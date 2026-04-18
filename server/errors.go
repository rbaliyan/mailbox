package server

import (
	"errors"

	"github.com/rbaliyan/mailbox"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// toGRPCError maps mailbox sentinel errors to appropriate gRPC status codes.
// Stable, client-safe messages are used for all known errors so that wrapped
// internal context (file paths, DB details, user IDs) is never leaked.
// Unrecognised errors become codes.Internal with the message "internal error".
func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	// Structured error types — check before sentinels because they wrap them.
	var ve *mailbox.ValidationError
	if errors.As(err, &ve) {
		return status.Errorf(codes.InvalidArgument, "validation: field=%s %s", ve.Field, ve.Message)
	}

	// Sentinel errors — stable messages, no err.Error() forwarding.
	switch {
	// Not found
	case errors.Is(err, mailbox.ErrNotFound):
		return status.Error(codes.NotFound, "message not found")
	case errors.Is(err, mailbox.ErrAttachmentNotFound):
		return status.Error(codes.NotFound, "attachment not found")

	// Permission / auth
	case errors.Is(err, mailbox.ErrUnauthorized):
		return status.Error(codes.PermissionDenied, "access denied")

	// Conflict / precondition
	case errors.Is(err, mailbox.ErrDuplicateEntry):
		return status.Error(codes.AlreadyExists, "duplicate entry")
	case errors.Is(err, mailbox.ErrNotInTrash):
		return status.Error(codes.FailedPrecondition, "message is not in trash")
	case errors.Is(err, mailbox.ErrAlreadyInTrash):
		return status.Error(codes.FailedPrecondition, "message is already in trash")
	case errors.Is(err, mailbox.ErrFolderMismatch):
		return status.Error(codes.FailedPrecondition, "message is not in the expected folder")
	case errors.Is(err, mailbox.ErrAlreadyConnected):
		return status.Error(codes.FailedPrecondition, "already connected")
	case errors.Is(err, mailbox.ErrUserResolveFailed):
		return status.Error(codes.FailedPrecondition, "user resolution failed")

	// Invalid argument
	case errors.Is(err, mailbox.ErrInvalidMessage):
		return status.Error(codes.InvalidArgument, "invalid message")
	case errors.Is(err, mailbox.ErrEmptyRecipients):
		return status.Error(codes.InvalidArgument, "at least one recipient is required")
	case errors.Is(err, mailbox.ErrEmptySubject):
		return status.Error(codes.InvalidArgument, "subject is required")
	case errors.Is(err, mailbox.ErrInvalidID):
		return status.Error(codes.InvalidArgument, "invalid message ID")
	case errors.Is(err, mailbox.ErrInvalidUserID):
		return status.Error(codes.InvalidArgument, "invalid user ID")
	case errors.Is(err, mailbox.ErrFilterInvalid):
		return status.Error(codes.InvalidArgument, "invalid filter")
	case errors.Is(err, mailbox.ErrInvalidMetadata):
		return status.Error(codes.InvalidArgument, "invalid metadata")
	case errors.Is(err, mailbox.ErrMetadataKeyTooLong):
		return status.Error(codes.InvalidArgument, "metadata key too long")
	case errors.Is(err, mailbox.ErrMetadataTooLarge):
		return status.Error(codes.InvalidArgument, "metadata too large")
	case errors.Is(err, mailbox.ErrSubjectTooLong):
		return status.Error(codes.InvalidArgument, "subject too long")
	case errors.Is(err, mailbox.ErrBodyTooLarge):
		return status.Error(codes.InvalidArgument, "body too large")
	case errors.Is(err, mailbox.ErrInvalidContent):
		return status.Error(codes.InvalidArgument, "invalid content")
	case errors.Is(err, mailbox.ErrTooManyRecipients):
		return status.Error(codes.InvalidArgument, "too many recipients")
	case errors.Is(err, mailbox.ErrInvalidRecipient):
		return status.Error(codes.InvalidArgument, "invalid recipient")
	case errors.Is(err, mailbox.ErrTooManyAttachments):
		return status.Error(codes.InvalidArgument, "too many attachments")
	case errors.Is(err, mailbox.ErrAttachmentTooLarge):
		return status.Error(codes.InvalidArgument, "attachment too large")
	case errors.Is(err, mailbox.ErrInvalidAttachment):
		return status.Error(codes.InvalidArgument, "invalid attachment")
	case errors.Is(err, mailbox.ErrInvalidMIMEType):
		return status.Error(codes.InvalidArgument, "invalid MIME type")
	case errors.Is(err, mailbox.ErrInvalidHeaders):
		return status.Error(codes.InvalidArgument, "invalid headers")
	case errors.Is(err, mailbox.ErrTooManyHeaders):
		return status.Error(codes.InvalidArgument, "too many headers")
	case errors.Is(err, mailbox.ErrHeaderKeyTooLong):
		return status.Error(codes.InvalidArgument, "header key too long")
	case errors.Is(err, mailbox.ErrHeaderValueTooLong):
		return status.Error(codes.InvalidArgument, "header value too long")
	case errors.Is(err, mailbox.ErrHeadersTooLarge):
		return status.Error(codes.InvalidArgument, "headers too large")
	case errors.Is(err, mailbox.ErrInvalidFolderID):
		return status.Error(codes.InvalidArgument, "invalid folder ID")
	case errors.Is(err, mailbox.ErrInvalidIdempotencyKey):
		return status.Error(codes.InvalidArgument, "invalid idempotency key")
	case errors.Is(err, mailbox.ErrInvalidTTL):
		return status.Error(codes.InvalidArgument, "invalid TTL")
	case errors.Is(err, mailbox.ErrInvalidSchedule):
		return status.Error(codes.InvalidArgument, "invalid schedule")

	// Resource exhausted
	case errors.Is(err, mailbox.ErrRateLimited):
		return status.Error(codes.ResourceExhausted, "rate limited")
	case errors.Is(err, mailbox.ErrQuotaExceeded):
		return status.Error(codes.ResourceExhausted, "quota exceeded")

	// Unavailable / not connected
	case errors.Is(err, mailbox.ErrNotConnected):
		return status.Error(codes.Unavailable, "service not connected")

	// Unimplemented
	case errors.Is(err, mailbox.ErrAttachmentStoreNotConfigured):
		return status.Error(codes.Unimplemented, "attachment store not configured")
	case errors.Is(err, mailbox.ErrNotifierNotConfigured):
		return status.Error(codes.Unimplemented, "notifier not configured")

	default:
		return status.Error(codes.Internal, "internal error")
	}
}
