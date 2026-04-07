package mailbox

import (
	"context"
	"fmt"

	"github.com/rbaliyan/mailbox/store"
)

// opAndPublish wraps a DB operation with event publishing.
// Outbox path: wraps op + publish in a store transaction; the bus auto-routes
// Event.Publish() to the outbox table via event.WithOutboxTx set by the store.
// Direct path: runs op, then publishes typed event directly.
func (s *service) opAndPublish(ctx context.Context, eventName, messageID string, eventData any, op func(ctx context.Context) error) error {
	if outbox, ok := s.store.(store.OutboxPersister); ok && outbox.OutboxEnabled() {
		return outbox.WithOutboxCtx(ctx, func(txCtx context.Context) error {
			if err := op(txCtx); err != nil {
				return err
			}
			pubCtx := ctxWithMessageID(txCtx, messageID)
			return s.publishTypedEvent(pubCtx, eventName, eventData)
		})
	}

	// Direct path: run op, then publish typed event.
	if err := op(ctx); err != nil {
		return err
	}
	pubCtx := ctxWithMessageID(ctx, messageID)
	if err := s.publishTypedEvent(pubCtx, eventName, eventData); err != nil {
		if s.opts.eventErrorsFatal {
			return &EventPublishError{Event: eventName, MessageID: messageID, Err: err}
		}
		s.opts.safeEventPublishFailure(eventName, err)
	}
	return nil
}

// publishOnly publishes an event without wrapping a DB operation.
// Used when the DB write already happened (e.g., batch operations, finalization).
// The bus routes to outbox automatically if inside a transaction context,
// or publishes directly to transport otherwise.
func (s *service) publishOnly(ctx context.Context, eventName, messageID string, eventData any) error {
	pubCtx := ctxWithMessageID(ctx, messageID)
	if err := s.publishTypedEvent(pubCtx, eventName, eventData); err != nil {
		if s.opts.eventErrorsFatal {
			return &EventPublishError{Event: eventName, MessageID: messageID, Err: err}
		}
		s.opts.safeEventPublishFailure(eventName, err)
	}
	return nil
}

// publishTypedEvent publishes a typed event directly without json round-trip.
func (s *service) publishTypedEvent(ctx context.Context, name string, data any) error {
	switch d := data.(type) {
	case MessageSentEvent:
		return s.events.MessageSent.Publish(ctx, d)
	case MessageReceivedEvent:
		return s.events.MessageReceived.Publish(ctx, d)
	case MessageReadEvent:
		return s.events.MessageRead.Publish(ctx, d)
	case MessageDeletedEvent:
		return s.events.MessageDeleted.Publish(ctx, d)
	case MessageMovedEvent:
		return s.events.MessageMoved.Publish(ctx, d)
	case MarkAllReadEvent:
		return s.events.MarkAllRead.Publish(ctx, d)
	default:
		return fmt.Errorf("unknown event type: %T", data)
	}
}
