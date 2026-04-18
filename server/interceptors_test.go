package server_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/rbaliyan/mailbox/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeStream is a minimal grpc.ServerStream whose Context() can be set.
// Other methods are embedded as nil interface — safe as long as tests
// don't trigger calls to them.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

// --- AuthInterceptor (unary) ---

func TestAuthInterceptor_AllowAll(t *testing.T) {
	t.Parallel()
	interceptor := server.AuthInterceptor(server.AllowAll())
	called := false
	_, err := interceptor(
		context.Background(), nil,
		&grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			called = true
			if _, ok := server.IdentityFromContext(ctx); !ok {
				t.Error("want Identity in context, got none")
			}
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestAuthInterceptor_DenyAll(t *testing.T) {
	t.Parallel()
	interceptor := server.AuthInterceptor(server.DenyAll())
	_, err := interceptor(
		context.Background(), nil,
		&grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			t.Error("handler must not be called when auth fails")
			return nil, nil
		},
	)
	if err == nil {
		t.Fatal("want auth error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Errorf("code: want %v, got %v", codes.Unauthenticated, st.Code())
	}
}

// --- StreamAuthInterceptor ---

func TestStreamAuthInterceptor_AllowAll(t *testing.T) {
	t.Parallel()
	interceptor := server.StreamAuthInterceptor(server.AllowAll())
	called := false
	err := interceptor(
		nil, &fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{},
		func(_ any, ss grpc.ServerStream) error {
			called = true
			if _, ok := server.IdentityFromContext(ss.Context()); !ok {
				t.Error("want Identity in stream context, got none")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestStreamAuthInterceptor_DenyAll(t *testing.T) {
	t.Parallel()
	interceptor := server.StreamAuthInterceptor(server.DenyAll())
	err := interceptor(
		nil, &fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{},
		func(_ any, _ grpc.ServerStream) error {
			t.Error("handler must not be called when auth fails")
			return nil
		},
	)
	if err == nil {
		t.Fatal("want auth error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Errorf("code: want %v, got %v", codes.Unauthenticated, st.Code())
	}
}

// --- RecoveryInterceptor (unary) ---

func TestRecoveryInterceptor_Panic(t *testing.T) {
	t.Parallel()
	interceptor := server.RecoveryInterceptor(slog.Default())
	_, err := interceptor(
		context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/test/Panic"},
		func(context.Context, any) (any, error) {
			panic("handler explosion")
		},
	)
	if err == nil {
		t.Fatal("want error from recovered panic, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Internal {
		t.Errorf("code: want %v, got %v", codes.Internal, st.Code())
	}
}

func TestRecoveryInterceptor_NoPanic(t *testing.T) {
	t.Parallel()
	interceptor := server.RecoveryInterceptor(slog.Default())
	_, err := interceptor(
		context.Background(), nil,
		&grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return nil, nil },
	)
	if err != nil {
		t.Fatalf("want no error on success, got %v", err)
	}
}

// --- StreamRecoveryInterceptor ---

func TestStreamRecoveryInterceptor_Panic(t *testing.T) {
	t.Parallel()
	interceptor := server.StreamRecoveryInterceptor(slog.Default())
	err := interceptor(
		nil, &fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/test/StreamPanic"},
		func(_ any, _ grpc.ServerStream) error {
			panic("stream explosion")
		},
	)
	if err == nil {
		t.Fatal("want error from recovered panic, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Internal {
		t.Errorf("code: want %v, got %v", codes.Internal, st.Code())
	}
}

func TestStreamRecoveryInterceptor_NoPanic(t *testing.T) {
	t.Parallel()
	interceptor := server.StreamRecoveryInterceptor(slog.Default())
	err := interceptor(
		nil, &fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{},
		func(_ any, _ grpc.ServerStream) error { return nil },
	)
	if err != nil {
		t.Fatalf("want no error on success, got %v", err)
	}
}
