package store

import (
	"context"

	event "github.com/rbaliyan/event/v3"
)

// OutboxPersister is an optional interface for stores that support
// wrapping mutations in a database transaction with outbox context.
//
// When outbox is enabled, the service layer calls WithOutboxCtx to wrap
// mutation + event publish in a single database transaction. The store
// sets event.WithOutboxTx on the context so the event bus automatically
// routes Event.Publish() calls to the outbox table within the same transaction.
//
// The background relay (from the event library) reads from the outbox and
// publishes to the event transport.
type OutboxPersister interface {
	// OutboxEnabled returns whether the outbox is configured.
	OutboxEnabled() bool

	// WithOutboxCtx wraps fn in a database transaction. The context passed
	// to fn has both the store's transaction and event.WithOutboxTx set,
	// so store methods use the same tx and Event.Publish() routes to outbox.
	// If outbox is disabled, calls fn directly with zero overhead.
	WithOutboxCtx(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventOutboxProvider is an optional interface for stores that can provide
// an event.OutboxStore for bus-level outbox integration. When implemented,
// the service configures the event bus with event.WithOutbox(store) so that
// Event.Publish() calls inside transactions are automatically routed to the
// outbox table.
type EventOutboxProvider interface {
	// EventOutboxStore returns an event.OutboxStore backed by the same database.
	// Returns nil if outbox is not enabled.
	EventOutboxStore() event.OutboxStore
}
