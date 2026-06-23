package server_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"

	"github.com/rbaliyan/event/v3/transport/channel"
	"github.com/rbaliyan/mailbox"
	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/server"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// errInternalProbe is an arbitrary non-sentinel error that must map to Internal.
var errInternalProbe = errors.New("backend exploded with internal detail")

// injectStore wraps an in-memory store and forces store.Get (and CreateMessages)
// to return a chosen sentinel error. All mailbox mutation methods load the
// message via store.Get first, so injecting on Get drives every mutation
// handler into its toGRPCError branch with the chosen sentinel.
type injectStore struct {
	*memory.Store
	getErr    error
	createErr error
}

func (s *injectStore) Get(ctx context.Context, id string) (store.Message, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.Store.Get(ctx, id)
}

func (s *injectStore) CreateMessage(ctx context.Context, data store.MessageData) (store.Message, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.Store.CreateMessage(ctx, data)
}

// startInjectServer starts a server backed by the given injecting store.
func startInjectServer(t *testing.T, st store.Store) mailboxpb.MailboxServiceClient {
	t.Helper()

	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(st),
		mailbox.WithEventTransport(channel.New()),
	)
	if err != nil {
		t.Fatalf("mailbox.New: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	srv, err := server.New(svc, server.WithSecurityGuard(server.AllowAll()))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(server.AuthInterceptor(server.AllowAll())))
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

// TestServer_HandlerErrorMapping injects a sentinel via store.Get and asserts
// each mutation handler surfaces the expected gRPC code through toGRPCError.
func TestServer_HandlerErrorMapping(t *testing.T) {
	const id = "msg-1"

	type call func(c mailboxpb.MailboxServiceClient) error
	calls := map[string]call{
		"UpdateFlags": func(c mailboxpb.MailboxServiceClient) error {
			_, err := c.UpdateFlags(context.Background(), &mailboxpb.UpdateFlagsRequest{
				UserId: "alice", MessageId: id, Flags: &mailboxpb.Flags{Read: wrapperspb.Bool(true)},
			})
			return err
		},
		"MoveMessage": func(c mailboxpb.MailboxServiceClient) error {
			_, err := c.MoveMessage(context.Background(), &mailboxpb.MoveMessageRequest{
				UserId: "alice", MessageId: id, FolderId: store.FolderArchived,
			})
			return err
		},
		"DeleteMessage": func(c mailboxpb.MailboxServiceClient) error {
			_, err := c.DeleteMessage(context.Background(), &mailboxpb.DeleteMessageRequest{
				UserId: "alice", MessageId: id,
			})
			return err
		},
		"RestoreMessage": func(c mailboxpb.MailboxServiceClient) error {
			_, err := c.RestoreMessage(context.Background(), &mailboxpb.RestoreMessageRequest{
				UserId: "alice", MessageId: id,
			})
			return err
		},
		"PermanentlyDeleteMessage": func(c mailboxpb.MailboxServiceClient) error {
			_, err := c.PermanentlyDeleteMessage(context.Background(), &mailboxpb.PermanentlyDeleteMessageRequest{
				UserId: "alice", MessageId: id,
			})
			return err
		},
		"AddTag": func(c mailboxpb.MailboxServiceClient) error {
			_, err := c.AddTag(context.Background(), &mailboxpb.TagRequest{
				UserId: "alice", MessageId: id, TagId: "t",
			})
			return err
		},
		"RemoveTag": func(c mailboxpb.MailboxServiceClient) error {
			_, err := c.RemoveTag(context.Background(), &mailboxpb.TagRequest{
				UserId: "alice", MessageId: id, TagId: "t",
			})
			return err
		},
		"GetMessage": func(c mailboxpb.MailboxServiceClient) error {
			_, err := c.GetMessage(context.Background(), &mailboxpb.GetMessageRequest{
				UserId: "alice", MessageId: id,
			})
			return err
		},
	}

	sentinels := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{"not found", mailbox.ErrNotFound, codes.NotFound},
		{"duplicate", mailbox.ErrDuplicateEntry, codes.AlreadyExists},
		{"not in trash", mailbox.ErrNotInTrash, codes.FailedPrecondition},
		{"rate limited", mailbox.ErrRateLimited, codes.ResourceExhausted},
		{"unknown", errInternalProbe, codes.Internal},
	}

	for callName, fn := range calls {
		for _, sc := range sentinels {
			t.Run(callName+"/"+sc.name, func(t *testing.T) {
				st := &injectStore{Store: memory.New(), getErr: sc.err}
				client := startInjectServer(t, st)
				err := fn(client)
				if err == nil {
					t.Fatalf("%s: expected error, got nil", callName)
				}
				if code := status.Code(err); code != sc.wantCode {
					t.Errorf("%s: code = %v, want %v", callName, code, sc.wantCode)
				}
			})
		}
	}
}

// TestServer_SendMessage_StoreError drives the SendMessage handler's error path
// via an injected CreateMessages failure.
func TestServer_SendMessage_StoreError(t *testing.T) {
	st := &injectStore{Store: memory.New(), createErr: mailbox.ErrQuotaExceeded}
	client := startInjectServer(t, st)

	_, err := client.SendMessage(context.Background(), &mailboxpb.SendMessageRequest{
		UserId:       "alice",
		RecipientIds: []string{"bob"},
		Subject:      "subject",
		Body:         "body",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := status.Code(err); code != codes.ResourceExhausted {
		t.Errorf("code = %v, want ResourceExhausted", code)
	}
}

// --- LoggingInterceptor / StreamLoggingInterceptor (interceptors.go) ---

func TestLoggingInterceptor_SuccessAndError(t *testing.T) {
	t.Parallel()
	interceptor := server.LoggingInterceptor(slog.Default())

	// Success path.
	if _, err := interceptor(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/test/OK"},
		func(context.Context, any) (any, error) { return "ok", nil },
	); err != nil {
		t.Fatalf("success path returned error: %v", err)
	}

	// Error path.
	wantErr := status.Error(codes.Internal, "boom")
	if _, err := interceptor(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/test/Err"},
		func(context.Context, any) (any, error) { return nil, wantErr },
	); err == nil {
		t.Fatal("error path should propagate the error")
	}
}

func TestStreamLoggingInterceptor_SuccessAndError(t *testing.T) {
	t.Parallel()
	interceptor := server.StreamLoggingInterceptor(slog.Default())

	if err := interceptor(nil, &fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/test/StreamOK"},
		func(any, grpc.ServerStream) error { return nil },
	); err != nil {
		t.Fatalf("success path returned error: %v", err)
	}

	wantErr := status.Error(codes.Internal, "boom")
	if err := interceptor(nil, &fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/test/StreamErr"},
		func(any, grpc.ServerStream) error { return wantErr },
	); err == nil {
		t.Fatal("error path should propagate the error")
	}
}

// --- security.go: DenyAll.Authorize and anonymous identity accessors ---

func TestSecurity_DenyAllAuthorizeAndIdentity(t *testing.T) {
	t.Parallel()

	// DenyAll().Authorize always denies.
	dec, err := server.DenyAll().Authorize(context.Background(), nil, server.ActionRead, server.Resource{OwnerID: "alice"})
	if err != nil {
		t.Fatalf("Authorize returned error: %v", err)
	}
	if dec.Allowed {
		t.Error("DenyAll.Authorize should not allow")
	}
	if dec.Reason == "" {
		t.Error("DenyAll.Authorize should give a reason")
	}

	// AllowAll authenticates to an anonymous identity whose accessors are covered.
	id, err := server.AllowAll().Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.UserID() != "anonymous" {
		t.Errorf("UserID = %q, want anonymous", id.UserID())
	}
	if id.Claims() != nil {
		t.Errorf("Claims = %v, want nil", id.Claims())
	}
}
