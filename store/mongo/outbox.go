package mongo

import (
	"context"
	"fmt"

	event "github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event-mongodb/outbox"
	"github.com/rbaliyan/mailbox/store"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Compile-time checks.
var (
	_ store.OutboxPersister     = (*Store)(nil)
	_ store.EventOutboxProvider = (*Store)(nil)
)

// OutboxEnabled returns whether the outbox is configured.
func (s *Store) OutboxEnabled() bool {
	return s.opts.outboxEnabled
}

// WithOutboxCtx wraps fn in a MongoDB transaction that is shared by both
// store methods (via the session context) and the event bus (via event.WithOutboxTx).
// This enables atomic mutation + event publish: store methods use the session
// for DB writes, and Event.Publish() routes to the outbox collection in the same tx.
//
// If the context already carries a MongoDB session (e.g., from an external transaction),
// the existing session is reused instead of starting a new one. This allows callers to
// include mailbox operations in a broader transaction that spans multiple collections.
//
// Zero overhead when outbox is disabled.
func (s *Store) WithOutboxCtx(ctx context.Context, fn func(ctx context.Context) error) error {
	if !s.opts.outboxEnabled {
		return fn(ctx)
	}

	// If already inside a MongoDB transaction, reuse it.
	// This allows external callers to start a transaction and include
	// mailbox operations + event outbox writes in the same tx.
	if sess := mongo.SessionFromContext(ctx); sess != nil {
		txCtx := event.WithOutboxTx(ctx, ctx)
		return fn(txCtx)
	}

	session, err := s.client.StartSession()
	if err != nil {
		// Standalone topology fallback: run without transaction.
		if !isTransactionNotSupported(err) {
			return fmt.Errorf("start outbox session: %w", err)
		}
		return fn(ctx)
	}
	defer session.EndSession(ctx)

	_, txErr := session.WithTransaction(ctx, func(sessCtx context.Context) (any, error) {
		// Set event outbox tx so bus routes Event.Publish() to outbox.
		txCtx := event.WithOutboxTx(sessCtx, sessCtx)
		return nil, fn(txCtx)
	})
	if txErr != nil && isTransactionNotSupported(txErr) {
		// Standalone fallback: run without transaction.
		return fn(ctx)
	}
	return txErr
}

// EventOutboxStore returns an event.OutboxStore backed by the same MongoDB
// database, for configuring the event bus with event.WithOutbox().
// Returns nil if outbox is not enabled.
func (s *Store) EventOutboxStore() event.OutboxStore {
	if !s.opts.outboxEnabled {
		return nil
	}
	mongoStore, err := outbox.NewMongoStore(s.db, outbox.WithCollection(s.opts.outboxCollection))
	if err != nil {
		s.logger.Error("failed to create event outbox store", "error", err)
		return nil
	}
	return mongoStore
}
