package mongo

import (
	"context"
	"fmt"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Compile-time check.
var _ store.OutboxPersister = (*Store)(nil)

// outboxDoc is the MongoDB document structure for outbox events.
type outboxDoc struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	Name      string        `bson:"name"`
	MessageID string        `bson:"message_id"`
	Payload   []byte        `bson:"payload"`
	Status    string        `bson:"status"` // "pending", "published", "failed"
	CreatedAt time.Time     `bson:"created_at"`
}

// WithOutboxCtx wraps a mutation function with transactional outbox support.
// If outbox is disabled or no events are in context, calls fn directly (zero overhead).
// If outbox is enabled and events exist, wraps fn + outbox insert in a MongoDB transaction.
//
// Usage from service layer:
//
//	ctx = store.WithOutboxEvents(ctx, event1, event2)
//	err = s.store.WithOutboxCtx(ctx, func(ctx context.Context) error {
//	    return s.store.MoveToFolder(ctx, id, folder)
//	})
func (s *Store) WithOutboxCtx(ctx context.Context, fn func(ctx context.Context) error) error {
	if !s.opts.outboxEnabled {
		return fn(ctx)
	}

	events := store.OutboxEventsFromCtx(ctx)
	if len(events) == 0 {
		return fn(ctx)
	}

	// Wrap in a transaction: fn + outbox insert are atomic.
	session, err := s.client.StartSession()
	if err != nil {
		// Only fall back for standalone topology; propagate other errors.
		if !isTransactionNotSupported(err) {
			return fmt.Errorf("start outbox session: %w", err)
		}
		if err := fn(ctx); err != nil {
			return err
		}
		return s.writeOutboxEvents(ctx, events)
	}
	defer session.EndSession(ctx)

	var fnExecuted bool
	_, txErr := session.WithTransaction(ctx, func(sessCtx context.Context) (any, error) {
		fnExecuted = true
		if err := fn(sessCtx); err != nil {
			return nil, err
		}
		return nil, s.writeOutboxEvents(sessCtx, events)
	})
	if txErr != nil && isTransactionNotSupported(txErr) {
		// Standalone fallback. If fn already ran inside the failed tx,
		// its writes were rolled back, so re-executing is safe.
		// However, fn may have non-DB side effects — document this.
		if err := fn(ctx); err != nil {
			return err
		}
		return s.writeOutboxEvents(ctx, events)
	}
	_ = fnExecuted // used for documentation clarity
	return txErr
}

// writeOutboxEvents inserts events into the outbox collection.
func (s *Store) writeOutboxEvents(ctx context.Context, events []store.OutboxEvent) error {
	outboxColl := s.db.Collection(s.opts.outboxCollection)
	now := time.Now().UTC()

	docs := make([]any, len(events))
	for i, evt := range events {
		docs[i] = outboxDoc{
			Name:      evt.Name,
			MessageID: evt.MessageID,
			Payload:   evt.Payload,
			Status:    "pending",
			CreatedAt: now,
		}
	}

	_, err := outboxColl.InsertMany(ctx, docs)
	if err != nil {
		return fmt.Errorf("write outbox events: %w", err)
	}
	return nil
}

// OutboxEnabled returns whether the outbox is configured.
func (s *Store) OutboxEnabled() bool {
	return s.opts.outboxEnabled
}

// PersistOutboxEvents writes events directly to the outbox collection without
// wrapping a database transaction. Used for event-only operations where
// no other mutation needs atomicity.
func (s *Store) PersistOutboxEvents(ctx context.Context, events ...store.OutboxEvent) error {
	if !s.opts.outboxEnabled || len(events) == 0 {
		return nil
	}
	return s.writeOutboxEvents(ctx, events)
}
