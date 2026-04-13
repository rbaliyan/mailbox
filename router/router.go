// Package router defines interfaces for multi-mailbox routing and registration.
//
// Router resolves a user ID to the mailbox ID that owns that user's messages.
// Registrar is called by a mailbox instance during Connect to announce itself
// so routers can discover it. The two interfaces typically share a backing
// registry (database, service, etc.) but are defined separately because they
// have distinct consumers: Routers are consulted by orchestrators to decide
// where to deliver messages, while Registrars are called by the mailbox
// instance itself during its own lifecycle.
package router

import "context"

// Router resolves user IDs to mailbox IDs.
// Given a user ID, Route returns the mailbox ID that owns that user's messages.
// Implementations should be safe for concurrent use.
type Router interface {
	// Route returns the mailbox ID for the given user.
	// Returns an error if the user is unknown or the lookup fails.
	Route(ctx context.Context, userID string) (string, error)
}

// Registrar registers a mailbox instance in a shared registry so routers can
// discover it. A mailbox calls Register during Connect; if Register returns
// an error, Connect fails. The returned mailbox ID is assigned by the
// registrar and stored on the service for use during its lifecycle.
//
// Implementations should be safe for concurrent use.
type Registrar interface {
	// Register announces this mailbox instance and returns the mailbox ID
	// assigned by the registrar. Returning an error aborts the mailbox's Connect.
	Register(ctx context.Context) (mailboxID string, err error)
}
