package server

import (
	"context"
	"errors"

	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/notify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// StreamNotifications streams real-time notification events for a user.
// The stream stays open until the client disconnects, the server context is
// cancelled, or WithStreamMaxDuration elapses (forcing a reconnect cycle).
// Use last_event_id to replay events missed since the last connection ("" for
// new events only).
func (s *Server) StreamNotifications(req *mailboxpb.StreamNotificationsRequest, stream mailboxpb.MailboxService_StreamNotificationsServer) error {
	ctx := stream.Context()

	if err := requireUserID(req.UserId); err != nil {
		return err
	}
	if err := s.authorize(ctx, ActionRead, req.UserId); err != nil {
		return err
	}

	// Enforce a maximum stream duration to prevent half-open connections from
	// holding goroutines indefinitely. Clients reconnect transparently on EOF.
	if s.opts.streamMaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.streamMaxDuration)
		defer cancel()
	}

	notifStream, err := s.svc.Notifications(ctx, req.UserId, req.LastEventId)
	if err != nil {
		return toGRPCError(err)
	}
	defer notifStream.Close() //nolint:errcheck

	for {
		evt, err := notifStream.Next(ctx)
		if err != nil {
			if errors.Is(err, notify.ErrStreamClosed) || errors.Is(err, notify.ErrNotifierClosed) {
				return status.Error(codes.Unavailable, "notification stream closed")
			}
			if ctx.Err() != nil {
				// Client disconnected, context cancelled, or max duration reached —
				// all are normal shutdown; return nil so gRPC sends clean EOF.
				return nil
			}
			return toGRPCError(err)
		}

		pbEvt := &mailboxpb.NotificationEvent{
			Id:        evt.ID,
			Type:      evt.Type,
			UserId:    evt.UserID,
			Payload:   evt.Payload,
			Timestamp: timestamppb.New(evt.Timestamp),
		}
		if err := stream.Send(pbEvt); err != nil {
			return err
		}
	}
}
