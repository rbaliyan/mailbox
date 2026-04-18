package server

import (
	"context"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuthInterceptor returns a unary interceptor that authenticates every request
// using the provided SecurityGuard. On success the Identity is stored in the
// context so RPC methods can call Authorize without a redundant Authenticate.
// On failure the request is rejected with codes.Unauthenticated.
func AuthInterceptor(guard SecurityGuard) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id, err := guard.Authenticate(ctx)
		if err != nil {
			return nil, err
		}
		return handler(ContextWithIdentity(ctx, id), req)
	}
}

// StreamAuthInterceptor returns a stream interceptor that authenticates every
// stream using the provided SecurityGuard.
func StreamAuthInterceptor(guard SecurityGuard) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		id, err := guard.Authenticate(ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ContextWithIdentity(ss.Context(), id)})
	}
}

// LoggingInterceptor returns a unary interceptor that logs each request at
// Debug level on success and Error level on failure.
func LoggingInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err != nil {
			logger.Error("grpc request failed", "method", info.FullMethod, "error", err)
		} else {
			logger.Debug("grpc request", "method", info.FullMethod)
		}
		return resp, err
	}
}

// StreamLoggingInterceptor returns a stream interceptor that logs each stream.
func StreamLoggingInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		err := handler(srv, ss)
		if err != nil {
			logger.Error("grpc stream failed", "method", info.FullMethod, "error", err)
		} else {
			logger.Debug("grpc stream completed", "method", info.FullMethod)
		}
		return err
	}
}

// RecoveryInterceptor returns a unary interceptor that catches panics, logs
// the stack trace, and returns codes.Internal to the caller.
func RecoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic recovered",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// StreamRecoveryInterceptor returns a stream interceptor that catches panics.
func StreamRecoveryInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic recovered",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}

// wrappedStream overrides Context() on a grpc.ServerStream so that the
// authenticated Identity propagates to all stream operations.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }
