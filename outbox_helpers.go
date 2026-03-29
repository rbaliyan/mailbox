package mailbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rbaliyan/mailbox/store"
)

// newOutboxEvent creates an OutboxEvent from a typed event payload.
func newOutboxEvent(name, messageID string, data any) (store.OutboxEvent, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return store.OutboxEvent{}, fmt.Errorf("marshal outbox event %s: %w", name, err)
	}
	return store.OutboxEvent{
		Name:      name,
		MessageID: messageID,
		Payload:   payload,
	}, nil
}

// opAndPublish wraps a DB operation with event publishing.
// Outbox path: serializes event, wraps op + outbox insert in store transaction.
// Direct path: runs op, then publishes typed event directly (no json round-trip).
func (s *service) opAndPublish(ctx context.Context, eventName, messageID string, eventData any, op func(ctx context.Context) error) error {
	if outbox, ok := s.store.(store.OutboxPersister); ok && outbox.OutboxEnabled() {
		evt, err := newOutboxEvent(eventName, messageID, eventData)
		if err != nil {
			return err
		}
		evtCtx := store.WithOutboxEvents(ctx, evt)
		return outbox.WithOutboxCtx(evtCtx, op)
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
// Outbox path: inserts directly into the outbox table (no transaction needed).
// Direct path: publishes typed event directly.
func (s *service) publishOnly(ctx context.Context, eventName, messageID string, eventData any) error {
	if outbox, ok := s.store.(store.OutboxPersister); ok && outbox.OutboxEnabled() {
		evt, err := newOutboxEvent(eventName, messageID, eventData)
		if err != nil {
			return err
		}
		return outbox.PersistOutboxEvents(ctx, evt)
	}

	// Direct path: publish typed event.
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

// PublishOutboxEvent publishes an OutboxEvent by decoding its JSON payload
// and dispatching to the correct typed event on the bus. This is the entry
// point for outbox relay implementations that read pending events from the
// outbox table and need to publish them to the event transport.
func (s *service) PublishOutboxEvent(ctx context.Context, evt store.OutboxEvent) error {
	switch evt.Name {
	case EventNameMessageSent:
		var data MessageSentEvent
		if err := json.Unmarshal(evt.Payload, &data); err != nil {
			return err
		}
		return s.events.MessageSent.Publish(ctx, data)
	case EventNameMessageReceived:
		var data MessageReceivedEvent
		if err := json.Unmarshal(evt.Payload, &data); err != nil {
			return err
		}
		return s.events.MessageReceived.Publish(ctx, data)
	case EventNameMessageRead:
		var data MessageReadEvent
		if err := json.Unmarshal(evt.Payload, &data); err != nil {
			return err
		}
		return s.events.MessageRead.Publish(ctx, data)
	case EventNameMessageDeleted:
		var data MessageDeletedEvent
		if err := json.Unmarshal(evt.Payload, &data); err != nil {
			return err
		}
		return s.events.MessageDeleted.Publish(ctx, data)
	case EventNameMessageMoved:
		var data MessageMovedEvent
		if err := json.Unmarshal(evt.Payload, &data); err != nil {
			return err
		}
		return s.events.MessageMoved.Publish(ctx, data)
	case EventNameMarkAllRead:
		var data MarkAllReadEvent
		if err := json.Unmarshal(evt.Payload, &data); err != nil {
			return err
		}
		return s.events.MarkAllRead.Publish(ctx, data)
	default:
		return fmt.Errorf("unknown outbox event: %s", evt.Name)
	}
}
