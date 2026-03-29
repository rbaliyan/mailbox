package store

import "context"

// OutboxEvent is a pending event to be written to the outbox table
// atomically with the main DB operation. The service layer attaches
// these to the context via WithOutboxEvents; store implementations
// check for them and write atomically when outbox is enabled.
type OutboxEvent struct {
	Name      string // event name (e.g., "mailbox.message.sent")
	MessageID string // coalescing key
	Payload   []byte // JSON-encoded event data
}

type outboxCtxKey struct{}

// WithOutboxEvents returns a context carrying pending outbox events.
// Store implementations check for these and persist them atomically
// with the main operation when outbox is enabled.
func WithOutboxEvents(ctx context.Context, events ...OutboxEvent) context.Context {
	existing := OutboxEventsFromCtx(ctx)
	all := make([]OutboxEvent, 0, len(existing)+len(events))
	all = append(all, existing...)
	all = append(all, events...)
	return context.WithValue(ctx, outboxCtxKey{}, all)
}

// OutboxEventsFromCtx extracts pending outbox events from context.
// Returns nil if no events are attached.
func OutboxEventsFromCtx(ctx context.Context) []OutboxEvent {
	events, _ := ctx.Value(outboxCtxKey{}).([]OutboxEvent)
	return events
}

// OutboxPersister is an optional interface for stores that support
// persisting outbox events atomically with mutations.
//
// The service layer:
//  1. Attaches events to context via WithOutboxEvents
//  2. Calls WithOutboxCtx wrapping the store mutation
//
// The store:
//  1. Accepts outbox config via its own options (e.g., mongostore.WithOutbox(true))
//  2. WithOutboxCtx wraps the mutation + outbox insert in a single DB transaction
//  3. Background relay reads from outbox and publishes to event transport
type OutboxPersister interface {
	// OutboxEnabled returns whether the outbox is configured.
	OutboxEnabled() bool

	// WithOutboxCtx wraps fn in a database transaction that also persists
	// any OutboxEvents found in ctx. If outbox is disabled or no events
	// are in context, calls fn directly with zero overhead.
	WithOutboxCtx(ctx context.Context, fn func(ctx context.Context) error) error

	// PersistOutboxEvents writes events directly to the outbox without
	// wrapping a database transaction. Use this for event-only operations
	// where no other database mutation needs atomicity (e.g., publishOnly).
	// Returns nil without writing if outbox is disabled or events is empty.
	PersistOutboxEvents(ctx context.Context, events ...OutboxEvent) error
}
