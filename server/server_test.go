package server_test

import (
	"context"
	"net"
	"testing"

	"github.com/rbaliyan/mailbox"
	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1 << 20

// startServer starts a gRPC server over a bufconn listener using the given
// SecurityGuard, and returns a connected client. Shut down via t.Cleanup.
func startServer(t *testing.T, guard server.SecurityGuard) mailboxpb.MailboxServiceClient {
	t.Helper()

	svc := mailboxtest.NewService(t, mailbox.Config{})
	srv, err := server.New(svc, server.WithSecurityGuard(guard))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			server.AuthInterceptor(guard),
		),
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

	return mailboxpb.NewMailboxServiceClient(conn)
}

// TestServer_AllowAll verifies that AllowAll guard permits any request.
func TestServer_AllowAll(t *testing.T) {
	t.Parallel()

	client := startServer(t, server.AllowAll())

	resp, err := client.ListDrafts(context.Background(), &mailboxpb.ListDraftsRequest{
		UserId: "alice",
	})
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	if resp == nil {
		t.Fatal("want non-nil response")
	}
}

// TestServer_DenyAll verifies that DenyAll guard rejects every request with
// codes.Unauthenticated.
func TestServer_DenyAll(t *testing.T) {
	t.Parallel()

	client := startServer(t, server.DenyAll())

	_, err := client.ListDrafts(context.Background(), &mailboxpb.ListDraftsRequest{
		UserId: "alice",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("want gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("code: want %v, got %v", codes.Unauthenticated, st.Code())
	}
}

// TestServer_MissingUserID verifies that an empty user_id is rejected with
// codes.InvalidArgument before authorization runs.
func TestServer_MissingUserID(t *testing.T) {
	t.Parallel()

	client := startServer(t, server.AllowAll())

	_, err := client.GetMessage(context.Background(), &mailboxpb.GetMessageRequest{
		UserId:    "",
		MessageId: "msg-123",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, st.Code())
	}
}

// TestServer_SearchRequiresQuery verifies that SearchMessages rejects an
// empty query with codes.InvalidArgument.
func TestServer_SearchRequiresQuery(t *testing.T) {
	t.Parallel()

	client := startServer(t, server.AllowAll())

	_, err := client.SearchMessages(context.Background(), &mailboxpb.SearchMessagesRequest{
		UserId: "alice",
		Query:  "",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, st.Code())
	}
}

// TestServer_ComposeSendDraft exercises the full draft lifecycle.
func TestServer_ComposeSendDraft(t *testing.T) {
	t.Parallel()

	client := startServer(t, server.AllowAll())
	ctx := context.Background()

	// Compose a draft.
	composeResp, err := client.ComposeDraft(ctx, &mailboxpb.ComposeDraftRequest{
		UserId:       "alice",
		Subject:      "Hello",
		Body:         "World",
		RecipientIds: []string{"bob"},
	})
	if err != nil {
		t.Fatalf("ComposeDraft: %v", err)
	}
	draftID := composeResp.Draft.GetId()
	if draftID == "" {
		t.Fatal("want non-empty draft ID")
	}
	if composeResp.Draft.GetOwnerId() != "alice" {
		t.Errorf("owner_id: want %q, got %q", "alice", composeResp.Draft.GetOwnerId())
	}

	// Verify it appears in the draft list.
	listResp, err := client.ListDrafts(ctx, &mailboxpb.ListDraftsRequest{UserId: "alice"})
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	if listResp.GetTotal() != 1 {
		t.Errorf("want 1 draft, got %d", listResp.GetTotal())
	}

	// Send the draft.
	msgResp, err := client.SendDraft(ctx, &mailboxpb.SendDraftRequest{
		UserId:  "alice",
		DraftId: draftID,
	})
	if err != nil {
		t.Fatalf("SendDraft: %v", err)
	}
	if msgResp.GetMessage().GetId() == "" {
		t.Fatal("want non-empty message ID after send")
	}

	// Draft should be gone after send.
	listResp2, err := client.ListDrafts(ctx, &mailboxpb.ListDraftsRequest{UserId: "alice"})
	if err != nil {
		t.Fatalf("ListDrafts after send: %v", err)
	}
	if listResp2.GetTotal() != 0 {
		t.Errorf("want 0 drafts after send, got %d", listResp2.GetTotal())
	}
}

// TestServer_DeleteDraft verifies that deleting a draft removes it.
func TestServer_DeleteDraft(t *testing.T) {
	t.Parallel()

	client := startServer(t, server.AllowAll())
	ctx := context.Background()

	composeResp, err := client.ComposeDraft(ctx, &mailboxpb.ComposeDraftRequest{
		UserId:  "alice",
		Subject: "To be deleted",
	})
	if err != nil {
		t.Fatalf("ComposeDraft: %v", err)
	}

	_, err = client.DeleteDraft(ctx, &mailboxpb.DeleteDraftRequest{
		UserId:  "alice",
		DraftId: composeResp.Draft.GetId(),
	})
	if err != nil {
		t.Fatalf("DeleteDraft: %v", err)
	}

	listResp, err := client.ListDrafts(ctx, &mailboxpb.ListDraftsRequest{UserId: "alice"})
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	if listResp.GetTotal() != 0 {
		t.Errorf("want 0 drafts after delete, got %d", listResp.GetTotal())
	}
}

// TestServer_GetDraft_NotFound verifies that fetching a non-existent draft
// returns codes.NotFound.
func TestServer_GetDraft_NotFound(t *testing.T) {
	t.Parallel()

	client := startServer(t, server.AllowAll())

	_, err := client.SendDraft(context.Background(), &mailboxpb.SendDraftRequest{
		UserId:  "alice",
		DraftId: "nonexistent-draft-id",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Errorf("code: want %v, got %v", codes.NotFound, st.Code())
	}
}
