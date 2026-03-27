// Package presence provides user online/offline tracking with optional routing.
//
// Presence tracking is designed as an independent module that can be used
// by notification systems, chat, or any feature that needs to know whether
// a user is currently connected and where to reach them.
//
// Routing information is optional. When provided via [RegisterOption], it is
// stored alongside the presence entry and can be retrieved via [Locate].
// This enables cross-instance notification delivery without polling.
//
// Implementations:
//   - memory: In-memory tracker for single-instance and testing.
//   - redis: Redis-backed tracker for multi-instance deployments.
package presence

import (
	"context"
	"errors"
)

// Sentinel errors for the presence package.
var (
	// ErrTrackerClosed is returned when the tracker has been closed.
	ErrTrackerClosed = errors.New("presence: tracker closed")

	// ErrNotFound is returned by Locate when the user has no active presence.
	ErrNotFound = errors.New("presence: user not found")
)

// RoutingInfo describes where a user is connected.
// The notification system uses this to route events to the correct instance.
type RoutingInfo struct {
	// InstanceID identifies the server instance holding the connection.
	InstanceID string `json:"instance_id,omitempty"`

	// Metadata holds arbitrary key-value routing data (e.g., address, port, region).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// RegisterOption configures a presence registration.
type RegisterOption func(*RegisterOptions)

// RegisterOptions holds parsed registration options.
// Implementations use ApplyRegisterOptions to parse them.
type RegisterOptions struct {
	Routing *RoutingInfo
}

// WithRouting attaches routing information to the presence registration.
// This enables cross-instance notification delivery via a Router.
func WithRouting(info RoutingInfo) RegisterOption {
	return func(o *RegisterOptions) {
		o.Routing = &info
	}
}

// ApplyRegisterOptions parses registration options.
func ApplyRegisterOptions(opts ...RegisterOption) *RegisterOptions {
	o := &RegisterOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Tracker tracks user online/offline status.
// Implementations must be safe for concurrent use.
type Tracker interface {
	// Register marks a user as online. Implementations should support
	// multiple concurrent registrations for the same user (e.g., multiple
	// browser tabs or devices). The user remains online as long as at
	// least one registration is active.
	Register(ctx context.Context, userID string, opts ...RegisterOption) (Registration, error)

	// IsOnline returns true if the user has at least one active registration.
	IsOnline(ctx context.Context, userID string) (bool, error)

	// OnlineUsers filters the given user IDs and returns only those currently online.
	OnlineUsers(ctx context.Context, userIDs []string) ([]string, error)

	// Locate returns routing information for an online user.
	// Returns ErrNotFound if the user has no active presence.
	// When multiple registrations exist, the implementation returns
	// the most recent routing info.
	Locate(ctx context.Context, userID string) (*RoutingInfo, error)

	// Close releases resources held by the tracker.
	Close(ctx context.Context) error
}

// Registration represents an active presence registration.
// Callers must call Unregister when the session ends (e.g., SSE disconnect).
// Implementations may automatically expire registrations after a TTL
// as a safety net for unclean disconnects.
type Registration interface {
	// Unregister marks this registration as inactive.
	// Safe to call multiple times.
	Unregister(ctx context.Context) error
}
