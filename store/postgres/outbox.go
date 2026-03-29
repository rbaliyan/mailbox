package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// Compile-time check.
var _ store.OutboxPersister = (*Store)(nil)

// OutboxEnabled returns whether the outbox is configured.
func (s *Store) OutboxEnabled() bool { return s.opts.outboxEnabled }

// WithOutboxCtx wraps fn in a PostgreSQL transaction that also persists
// any OutboxEvents from context. The transaction is injected into context
// via txCtxKey so that all store methods called within fn use the same tx.
// Zero overhead when outbox is disabled or no events are in context.
func (s *Store) WithOutboxCtx(ctx context.Context, fn func(ctx context.Context) error) error {
	if !s.opts.outboxEnabled {
		return fn(ctx)
	}

	events := store.OutboxEventsFromCtx(ctx)
	if len(events) == 0 {
		return fn(ctx)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin outbox transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Inject tx into context so store methods use it via s.exec(ctx).
	txCtx := context.WithValue(ctx, txCtxKey{}, tx)

	if err := fn(txCtx); err != nil {
		return err
	}

	// Write events to outbox table in same transaction.
	now := time.Now().UTC()
	for _, evt := range events {
		_, err := tx.ExecContext(txCtx,
			fmt.Sprintf(`INSERT INTO %s (name, message_id, payload, status, created_at) VALUES ($1, $2, $3, $4, $5)`, s.opts.outboxTable),
			evt.Name, evt.MessageID, evt.Payload, "pending", now,
		)
		if err != nil {
			return fmt.Errorf("write outbox event: %w", err)
		}
	}

	return tx.Commit()
}

// PersistOutboxEvents writes events directly to the outbox table without
// wrapping a database transaction. Used for event-only operations where
// no other mutation needs atomicity.
func (s *Store) PersistOutboxEvents(ctx context.Context, events ...store.OutboxEvent) error {
	if !s.opts.outboxEnabled || len(events) == 0 {
		return nil
	}

	now := time.Now().UTC()
	for _, evt := range events {
		_, err := s.db.ExecContext(ctx,
			fmt.Sprintf(`INSERT INTO %s (name, message_id, payload, status, created_at) VALUES ($1, $2, $3, $4, $5)`, s.opts.outboxTable),
			evt.Name, evt.MessageID, evt.Payload, "pending", now,
		)
		if err != nil {
			return fmt.Errorf("write outbox event: %w", err)
		}
	}
	return nil
}
