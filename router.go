package mailbox

import "context"

// Router resolves a user ID to the Mailbox that owns their messages.
//
// It decouples "where are this user's messages stored" from user identity,
// so user and mailbox registries can evolve and scale independently.
// Implementations might back this with a consistent-hash ring, a database
// shard table, or a single-node no-op that returns the local service's client.
//
// Implementations must be safe for concurrent use.
type Router interface {
	// Route returns the Mailbox that holds messages for the given user.
	// Returns an error if the user is unknown or the lookup fails.
	Route(ctx context.Context, userID string) (Mailbox, error)
}

// Registrar registers a mailbox instance in a shared registry so that
// Routers can discover it. A mailbox calls Register during Connect; if
// Register returns an error, Connect fails. The returned mailbox ID is
// assigned by the registrar and stored on the service for its lifecycle.
//
// Implementations must be safe for concurrent use.
type Registrar interface {
	// Register announces this mailbox instance and returns the mailbox ID
	// assigned by the registrar. Returning an error aborts Connect.
	Register(ctx context.Context) (mailboxID string, err error)
}
