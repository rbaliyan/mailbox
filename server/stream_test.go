package server_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/notify"
	notifymem "github.com/rbaliyan/mailbox/notify/memory"
	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/server"
	"github.com/rbaliyan/mailbox/store/memory"
	"github.com/rbaliyan/event/v3/transport/channel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// startStreamServer starts a gRPC server backed by a notifier-enabled service
// over bufconn, wiring both the unary and stream auth interceptors. It returns
// the client and the live service (so tests can trigger events directly).
func startStreamServer(t *testing.T, guard server.SecurityGuard) (mailboxpb.MailboxServiceClient, mailbox.Service) {
	t.Helper()

	notifier := notify.NewNotifier(
		notify.WithStore(notifymem.New()),
		notify.WithPollInterval(20*time.Millisecond),
	)
	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithNotifier(notifier),
	)
	if err != nil {
		t.Fatalf("mailbox.New: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	srv, err := server.New(svc, server.WithSecurityGuard(guard))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(server.AuthInterceptor(guard)),
		grpc.ChainStreamInterceptor(server.StreamAuthInterceptor(guard)),
	)
	mailboxpb.RegisterMailboxServiceServer(grpcSrv, srv)

	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() {
		grpcSrv.GracefulStop()
		_ = lis.Close()
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return mailboxpb.NewMailboxServiceClient(conn), svc
}

// TestServer_StreamNotifications_Delivery opens a streaming client, triggers a
// message send, and asserts the notification event is delivered over the wire.
func TestServer_StreamNotifications_Delivery(t *testing.T) {
	client, svc := startStreamServer(t, server.AllowAll())
	ctx := context.Background()

	stream, err := client.StreamNotifications(ctx, &mailboxpb.StreamNotificationsRequest{
		UserId: "bob",
	})
	if err != nil {
		t.Fatalf("StreamNotifications: %v", err)
	}

	// Trigger an event after the stream is open.
	if _, err := svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "ping",
		Body:         "pong",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Read until we observe the message-received event for bob.
	for {
		evt, err := stream.Recv()
		if err != nil {
			t.Fatalf("stream.Recv: %v", err)
		}
		if evt.GetType() == mailbox.EventNameMessageReceived {
			if evt.GetUserId() != "bob" {
				t.Errorf("event user = %q, want bob", evt.GetUserId())
			}
			break
		}
	}
}

// TestServer_StreamNotifications_ContextCancel verifies the stream shuts down
// cleanly (EOF, no error) when the client cancels its context.
func TestServer_StreamNotifications_ContextCancel(t *testing.T) {
	client, _ := startStreamServer(t, server.AllowAll())

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.StreamNotifications(ctx, &mailboxpb.StreamNotificationsRequest{
		UserId: "bob",
	})
	if err != nil {
		t.Fatalf("StreamNotifications: %v", err)
	}

	// Cancel the client context; the server returns nil -> clean EOF.
	cancel()

	_, recvErr := stream.Recv()
	if recvErr == nil {
		t.Fatal("expected an error after cancel, got nil")
	}
	// A cancelled client surfaces as io.EOF or codes.Canceled.
	st, _ := status.FromError(recvErr)
	if !errors.Is(recvErr, io.EOF) && st.Code() != codes.Canceled {
		t.Errorf("expected EOF or Canceled, got %v (code %v)", recvErr, st.Code())
	}
}

// TestServer_StreamNotifications_MissingUserID rejects an empty user_id before
// opening the stream.
func TestServer_StreamNotifications_MissingUserID(t *testing.T) {
	client, _ := startStreamServer(t, server.AllowAll())

	stream, err := client.StreamNotifications(context.Background(), &mailboxpb.StreamNotificationsRequest{})
	if err != nil {
		t.Fatalf("StreamNotifications open: %v", err)
	}
	_, recvErr := stream.Recv()
	if recvErr == nil {
		t.Fatal("expected error for empty user_id")
	}
	if st, _ := status.FromError(recvErr); st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}

// TestServer_StreamNotifications_Denied rejects an unauthorized streamer.
func TestServer_StreamNotifications_Denied(t *testing.T) {
	client, _ := startStreamServer(t, server.DenyAll())

	stream, err := client.StreamNotifications(context.Background(), &mailboxpb.StreamNotificationsRequest{
		UserId: "bob",
	})
	if err != nil {
		t.Fatalf("StreamNotifications open: %v", err)
	}
	_, recvErr := stream.Recv()
	if recvErr == nil {
		t.Fatal("expected auth error")
	}
	if st, _ := status.FromError(recvErr); st.Code() != codes.Unauthenticated {
		t.Errorf("code = %v, want Unauthenticated", st.Code())
	}
}

// TestServer_UpdateDraft covers the UpdateDraft handler: compose a draft, patch
// several fields, and verify the patched values round-trip.
func TestServer_UpdateDraft(t *testing.T) {
	client, _ := startStreamServer(t, server.AllowAll())
	ctx := context.Background()

	composeResp, err := client.ComposeDraft(ctx, &mailboxpb.ComposeDraftRequest{
		UserId:  "alice",
		Subject: "initial",
	})
	if err != nil {
		t.Fatalf("ComposeDraft: %v", err)
	}
	draftID := composeResp.GetDraft().GetId()

	updated, err := client.UpdateDraft(ctx, &mailboxpb.UpdateDraftRequest{
		UserId:       "alice",
		DraftId:      draftID,
		Subject:      "patched subject",
		Body:         "patched body",
		RecipientIds: []string{"bob"},
		Headers:      map[string]string{"X-Test": "1"},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if updated.GetDraft().GetSubject() != "patched subject" {
		t.Errorf("subject = %q", updated.GetDraft().GetSubject())
	}
	if updated.GetDraft().GetBody() != "patched body" {
		t.Errorf("body = %q", updated.GetDraft().GetBody())
	}

	// Missing fields are rejected.
	if _, err := client.UpdateDraft(ctx, &mailboxpb.UpdateDraftRequest{UserId: "alice"}); err == nil {
		t.Error("expected error for missing draft_id")
	}
	if _, err := client.UpdateDraft(ctx, &mailboxpb.UpdateDraftRequest{
		UserId:  "alice",
		DraftId: "nonexistent",
	}); err == nil {
		t.Error("expected error updating nonexistent draft")
	}
}
