package mailbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rbaliyan/mailbox/store"
)

// PendingEvent holds event data to be published after a DB operation.
// Events are collected during the operation and either written to the outbox
// (transactional path) or published directly (fallback path).
type PendingEvent struct {
	Name      string // event name (e.g., EventNameMessageSent)
	MessageID string // for context metadata (coalescing key)
	Data      any    // event payload struct
}

// txPublisher manages transactional event publishing.
// All transaction and outbox logic is centralized here.
//
// When outbox is enabled and the store supports transactions, events are
// inserted into the outbox within the same database transaction as the
// main operation — guaranteeing atomicity.
//
// When outbox is disabled (default), events are published directly after
// the operation — same behavior as before, fully backward compatible.
type txPublisher struct {
	service *service
	enabled bool // true when outbox mode is active
}

// withEvents executes fn (the DB operation) and then handles event publishing.
//
// With outbox enabled + transactional store:
//
//	Transaction {
//	    fn(ctx)              // DB writes
//	    outbox inserts       // event records
//	} // atomic commit
//	// relay publishes from outbox asynchronously
//
// Without outbox (default):
//
//	fn(ctx)                  // DB writes
//	publish events directly  // best-effort, current behavior
func (tp *txPublisher) withEvents(ctx context.Context, fn func(ctx context.Context) error, events ...PendingEvent) error {
	if len(events) == 0 {
		return fn(ctx)
	}

	if tp.enabled {
		return tp.transactionalPath(ctx, fn, events)
	}
	return tp.directPath(ctx, fn, events)
}

// directPath runs the operation then publishes events directly.
// This is the backward-compatible path used when outbox is not configured.
func (tp *txPublisher) directPath(ctx context.Context, fn func(ctx context.Context) error, events []PendingEvent) error {
	if err := fn(ctx); err != nil {
		return err
	}

	// Publish events best-effort (same as current behavior).
	for _, evt := range events {
		pubCtx := ctxWithMessageID(ctx, evt.MessageID)
		if err := tp.publishEvent(pubCtx, evt); err != nil {
			if tp.service.opts.eventErrorsFatal {
				return &EventPublishError{
					Event:     evt.Name,
					MessageID: evt.MessageID,
					Err:       err,
				}
			}
			tp.service.opts.safeEventPublishFailure(evt.Name, err)
		}
	}
	return nil
}

// transactionalPath wraps fn + outbox inserts in a single transaction.
func (tp *txPublisher) transactionalPath(ctx context.Context, fn func(ctx context.Context) error, events []PendingEvent) error {
	txStore, ok := tp.service.store.(store.TransactionalStore)
	if !ok {
		// Store doesn't support transactions — fall back to direct path.
		return tp.directPath(ctx, fn, events)
	}

	return txStore.WithTransaction(ctx, func(txCtx context.Context) error {
		// Run the DB operation within the transaction.
		if err := fn(txCtx); err != nil {
			return err
		}

		// Insert events into the outbox within the same transaction.
		// For now, we encode and publish via the event bus which routes
		// to the outbox store if configured. If no outbox store is
		// configured on the bus, this falls back to direct transport.
		for _, evt := range events {
			pubCtx := ctxWithMessageID(txCtx, evt.MessageID)
			if err := tp.publishEvent(pubCtx, evt); err != nil {
				return fmt.Errorf("outbox insert %s: %w", evt.Name, err)
			}
		}
		return nil
	})
}

// publishEvent publishes a single event by dispatching to the correct typed event.
func (tp *txPublisher) publishEvent(ctx context.Context, evt PendingEvent) error {
	svcEvents := tp.service.events

	switch data := evt.Data.(type) {
	case MessageSentEvent:
		return svcEvents.MessageSent.Publish(ctx, data)
	case MessageReceivedEvent:
		return svcEvents.MessageReceived.Publish(ctx, data)
	case MessageReadEvent:
		return svcEvents.MessageRead.Publish(ctx, data)
	case MessageDeletedEvent:
		return svcEvents.MessageDeleted.Publish(ctx, data)
	case MessageMovedEvent:
		return svcEvents.MessageMoved.Publish(ctx, data)
	case MarkAllReadEvent:
		return svcEvents.MarkAllRead.Publish(ctx, data)
	default:
		// Unknown event type — try JSON encoding as raw event.
		payload, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal event %s: %w", evt.Name, err)
		}
		_ = payload // raw publish not supported yet
		return fmt.Errorf("unknown event type for %s: %T", evt.Name, data)
	}
}

// newTxPublisher creates a txPublisher for the service.
func newTxPublisher(svc *service) *txPublisher {
	return &txPublisher{
		service: svc,
		enabled: svc.opts.outboxEnabled,
	}
}

