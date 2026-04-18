package server

import (
	"context"
	"fmt"

	"github.com/rbaliyan/mailbox"
	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// handleSendResult converts the (msg, err) pair returned by SendMessage or
// Draft.Send into a gRPC response. Partial delivery (some recipients failed
// but the message was created) is surfaced as success with a warning log so
// the caller receives the created message rather than an opaque error.
func (s *Server) handleSendResult(msg mailbox.Message, err error) (*mailboxpb.MessageResponse, error) {
	if err == nil {
		return &mailboxpb.MessageResponse{Message: s.toProtoMessage(msg)}, nil
	}
	pde, ok := mailbox.IsPartialDelivery(err)
	if !ok || msg == nil {
		return nil, toGRPCError(err)
	}
	s.opts.logger.Warn("partial delivery",
		"message_id", pde.MessageID,
		"delivered_to", pde.DeliveredTo,
		"failed_count", len(pde.FailedRecipients))
	return &mailboxpb.MessageResponse{Message: s.toProtoMessage(msg)}, nil
}

// Compile-time check that Server implements MailboxServiceServer.
var _ mailboxpb.MailboxServiceServer = (*Server)(nil)

// Server implements the MailboxServiceServer gRPC interface.
// Use New to create an instance and register it with a grpc.Server:
//
//	grpcServer := grpc.NewServer(...)
//	mailboxpb.RegisterMailboxServiceServer(grpcServer, server)
type Server struct {
	mailboxpb.UnimplementedMailboxServiceServer

	svc  mailbox.Service
	opts serverOptions
}

// New creates a Server backed by svc.
// The server is ready to be registered with a grpc.Server immediately; no
// additional lifecycle management is needed.
// Returns an error if svc is nil.
func New(svc mailbox.Service, opts ...Option) (*Server, error) {
	if svc == nil {
		return nil, fmt.Errorf("mailbox/server: New requires a non-nil Service")
	}
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	return &Server{svc: svc, opts: *o}, nil
}

// authorize extracts the Identity placed in ctx by AuthInterceptor and
// authorises the action on the resource owner.
//
// Returns codes.Unauthenticated if AuthInterceptor was not installed (no
// Identity in ctx). Tests should seed the context with ContextWithIdentity.
func (s *Server) authorize(ctx context.Context, action Action, ownerID string) error {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing authentication")
	}
	decision, err := s.opts.guard.Authorize(ctx, id, action, Resource{OwnerID: ownerID})
	if err != nil {
		s.opts.logger.Error("authorization check failed", "error", err, "action", action, "owner", ownerID)
		return status.Error(codes.Internal, "authorization error")
	}
	if !decision.Allowed {
		return status.Errorf(codes.PermissionDenied, "%s", decision.Reason)
	}
	return nil
}

// requireUserID returns InvalidArgument when userID is empty.
func requireUserID(userID string) error {
	if userID == "" {
		return status.Error(codes.InvalidArgument, "user_id is required")
	}
	return nil
}

// requireField returns InvalidArgument when value is empty.
func requireField(name, value string) error {
	if value == "" {
		return status.Errorf(codes.InvalidArgument, "%s is required", name)
	}
	return nil
}

// requireIDs returns InvalidArgument when ids is empty or exceeds maxBulkSize.
func (s *Server) requireIDs(field string, ids []string) error {
	if len(ids) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s must not be empty", field)
	}
	if len(ids) > s.opts.maxBulkSize {
		return status.Errorf(codes.InvalidArgument, "%s exceeds maximum of %d", field, s.opts.maxBulkSize)
	}
	return nil
}
