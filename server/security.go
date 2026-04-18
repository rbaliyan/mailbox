// Package server provides a gRPC service implementation for the mailbox library.
// Users wire it into their own gRPC server:
//
//	svc, _ := mailbox.New(cfg, mailbox.WithStore(store))
//	server, _ := server.New(svc, server.WithSecurityGuard(myGuard))
//
//	grpcServer := grpc.NewServer(
//	    grpc.ChainUnaryInterceptor(
//	        server.AuthInterceptor(myGuard),
//	        server.LoggingInterceptor(logger),
//	        server.RecoveryInterceptor(logger),
//	    ),
//	    grpc.ChainStreamInterceptor(
//	        server.StreamAuthInterceptor(myGuard),
//	        server.StreamLoggingInterceptor(logger),
//	        server.StreamRecoveryInterceptor(logger),
//	    ),
//	)
//	mailboxpb.RegisterMailboxServiceServer(grpcServer, server)
package server

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Identity represents an authenticated caller extracted from incoming RPC metadata.
type Identity interface {
	UserID() string
	Claims() map[string]any
}

// Decision is the outcome of an authorization check.
type Decision struct {
	Allowed bool
	Reason  string
}

// Resource identifies the mailbox resource being accessed.
// OwnerID is the user whose mailbox is the target of the operation.
// An empty OwnerID means the check is method-level only (no specific user).
type Resource struct {
	OwnerID string
}

// Action identifies the operation being authorized.
type Action string

// Action constants passed to SecurityGuard.Authorize.
const (
	ActionRead   Action = "read"
	ActionWrite  Action = "write"
	ActionDelete Action = "delete"
	ActionAdmin  Action = "admin"
)

// SecurityGuard handles authentication and authorization for every RPC.
//
// Authentication (Authenticate) runs once per RPC inside AuthInterceptor and
// stores the Identity in the context. Authorization (Authorize) runs inside
// each RPC method with the specific OwnerID and action being requested.
//
// Typical custom guard:
//
//	func (g *myGuard) Authenticate(ctx context.Context) (server.Identity, error) {
//	    md, _ := metadata.FromIncomingContext(ctx)
//	    token := md.Get("authorization")[0]
//	    claims, err := validateJWT(token)
//	    if err != nil {
//	        return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
//	    }
//	    return &myIdentity{userID: claims.Sub, claims: claims.Extra}, nil
//	}
//
//	func (g *myGuard) Authorize(_ context.Context, id server.Identity, action server.Action, res server.Resource) (server.Decision, error) {
//	    // allow users to access only their own mailbox
//	    if id.UserID() == res.OwnerID {
//	        return server.Decision{Allowed: true}, nil
//	    }
//	    return server.Decision{Allowed: false, Reason: "access denied"}, nil
//	}
type SecurityGuard interface {
	// Authenticate extracts and validates the caller's identity from the context.
	// Return a gRPC status error (e.g. codes.Unauthenticated) on failure.
	Authenticate(ctx context.Context) (Identity, error)

	// Authorize checks whether id may perform action on resource.
	// resource.OwnerID is the target mailbox user.
	Authorize(ctx context.Context, id Identity, action Action, resource Resource) (Decision, error)
}

type identityKey struct{}

// IdentityFromContext retrieves the Identity stored by AuthInterceptor.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// ContextWithIdentity returns a new context carrying id.
func ContextWithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// AllowAll returns a SecurityGuard that authenticates every caller as an
// anonymous identity and permits all actions. Use only in development or tests.
func AllowAll() SecurityGuard { return allowAllGuard{} }

type allowAllGuard struct{}

func (allowAllGuard) Authenticate(context.Context) (Identity, error) {
	return anonymousIdentity{}, nil
}
func (allowAllGuard) Authorize(context.Context, Identity, Action, Resource) (Decision, error) {
	return Decision{Allowed: true}, nil
}

// DenyAll returns a SecurityGuard whose Authenticate always fails with
// Unauthenticated. Use as a safe placeholder when no guard is configured.
func DenyAll() SecurityGuard { return denyAllGuard{} }

type denyAllGuard struct{}

func (denyAllGuard) Authenticate(context.Context) (Identity, error) {
	return nil, status.Error(codes.Unauthenticated, "no security guard configured")
}
func (denyAllGuard) Authorize(context.Context, Identity, Action, Resource) (Decision, error) {
	return Decision{Allowed: false, Reason: "no security guard configured"}, nil
}

type anonymousIdentity struct{}

func (anonymousIdentity) UserID() string         { return "anonymous" }
func (anonymousIdentity) Claims() map[string]any { return nil }
