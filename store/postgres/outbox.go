package postgres

import (
	"context"
	"fmt"

	event "github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event/v3/outbox"
	"github.com/rbaliyan/mailbox/store"
)

// Compile-time checks.
var (
	_ store.OutboxPersister     = (*Store)(nil)
	_ store.EventOutboxProvider = (*Store)(nil)
)

// OutboxEnabled returns whether the outbox is configured.
func (s *Store) OutboxEnabled() bool { return s.opts.outboxEnabled }

// WithOutboxCtx wraps fn in a PostgreSQL transaction that is shared by both
// store methods (via txCtxKey) and the event bus (via event.WithOutboxTx).
// This enables atomic mutation + event publish: store methods use the tx for
// DB writes, and Event.Publish() routes to the outbox table in the same tx.
// Zero overhead when outbox is disabled.
func (s *Store) WithOutboxCtx(ctx context.Context, fn func(ctx context.Context) error) error {
	if !s.opts.outboxEnabled {
		return fn(ctx)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin outbox transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Set both context keys so store methods and bus outbox use the same tx.
	txCtx := context.WithValue(ctx, txCtxKey{}, tx)
	txCtx = event.WithOutboxTx(txCtx, tx)

	if err := fn(txCtx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outbox transaction: %w", err)
	}
	return nil
}

// EventOutboxStore returns an event.OutboxStore backed by the same PostgreSQL
// database, for configuring the event bus with event.WithOutbox().
// Returns nil if outbox is not enabled.
func (s *Store) EventOutboxStore() event.OutboxStore {
	if !s.opts.outboxEnabled {
		return nil
	}
	// Create an event library outbox store pointing to the same DB and table.
	pgStore, err := outbox.NewPostgresStore(s.db.DB, outbox.WithTable(s.opts.outboxTable))
	if err != nil {
		s.logger.Error("failed to create event outbox store", "error", err)
		return nil
	}
	return pgStore
}
