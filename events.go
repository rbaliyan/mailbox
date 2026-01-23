package mailbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rbaliyan/event/v3"
)

// Event names for mailbox events.
//
// Events are OPTIONAL by default. If you don't call RegisterEvents(), all event
// publishing is silently skipped (no-op). This allows the library to work without
// any event infrastructure.
//
// To enable events, create a bus and register events:
//
//	bus, _ := event.NewBus("myapp", event.WithTransport(redis.New(client)))
//	mailbox.RegisterEvents(ctx, bus)
//
// For development/testing without event infrastructure:
//
//	bus, _ := event.NewBus("myapp", event.WithTransport(noop.New()))
//	mailbox.RegisterEvents(ctx, bus)
const (
	EventNameMessageSent    = "mailbox.message.sent"
	EventNameMessageRead    = "mailbox.message.read"
	EventNameMessageDeleted = "mailbox.message.deleted"
)

// MessageSentEvent is published when a message is sent.
// This is the primary event for notifying recipients of new messages.
type MessageSentEvent struct {
	MessageID    string    `json:"message_id"`
	SenderID     string    `json:"sender_id"`
	RecipientIDs []string  `json:"recipient_ids"`
	Subject      string    `json:"subject"`
	SentAt       time.Time `json:"sent_at"`
}

// MessageReadEvent is published when a message is marked as read.
// Use this for read receipts and tracking message engagement.
type MessageReadEvent struct {
	MessageID string    `json:"message_id"`
	UserID    string    `json:"user_id"`
	ReadAt    time.Time `json:"read_at"`
}

// MessageDeletedEvent is published when a message is permanently deleted.
// This event is only published for permanent deletions, not moves to trash.
type MessageDeletedEvent struct {
	MessageID string    `json:"message_id"`
	UserID    string    `json:"user_id"`
	DeletedAt time.Time `json:"deleted_at"`
}

// Mailbox events.
//
// Events are OPTIONAL - they use a no-op transport by default and silently
// skip publishing if RegisterEvents() has not been called. This design allows
// applications to use the mailbox library without any event infrastructure.
//
// To enable event publishing, call RegisterEvents() with a configured bus:
//
//	// Production: use Redis, NATS, Kafka, etc.
//	bus, _ := event.NewBus("myapp", event.WithTransport(redis.New(client)))
//	mailbox.RegisterEvents(ctx, bus)
//
//	// Development/testing: use noop transport (events silently dropped)
//	bus, _ := event.NewBus("myapp", event.WithTransport(noop.New()))
//	mailbox.RegisterEvents(ctx, bus)
//
// Subscribe to events:
//
//	mailbox.EventMessageSent.Subscribe(ctx, func(ctx context.Context, ev event.Event[MessageSentEvent], data MessageSentEvent) error {
//	    log.Printf("Message sent: %s", data.MessageID)
//	    return nil
//	})
var (
	// EventMessageSent is published when a message is sent.
	// This is the primary event for real-time notifications to recipients.
	EventMessageSent = event.New[MessageSentEvent](EventNameMessageSent)

	// EventMessageRead is published when a message is marked as read.
	// Use this for read receipts and delivery confirmation.
	EventMessageRead = event.New[MessageReadEvent](EventNameMessageRead)

	// EventMessageDeleted is published when a message is permanently deleted.
	// Only fired for permanent deletions, not trash operations.
	EventMessageDeleted = event.New[MessageDeletedEvent](EventNameMessageDeleted)
)

// registerEvents registers all mailbox events with the given bus.
// Events are global singletons - first registration wins, subsequent calls are no-ops.
func registerEvents(ctx context.Context, bus *event.Bus) error {
	events := []any{
		EventMessageSent,
		EventMessageRead,
		EventMessageDeleted,
	}

	for _, ev := range events {
		if err := registerEvent(ctx, bus, ev); err != nil {
			return err
		}
	}

	return nil
}

func registerEvent(ctx context.Context, bus *event.Bus, ev any) error {
	switch v := ev.(type) {
	case event.Event[MessageSentEvent]:
		return tryRegister(ctx, bus, v)
	case event.Event[MessageReadEvent]:
		return tryRegister(ctx, bus, v)
	case event.Event[MessageDeletedEvent]:
		return tryRegister(ctx, bus, v)
	default:
		// Return error for unknown event types to catch programming errors early.
		return fmt.Errorf("mailbox: unknown event type %T - update registerEvent switch", ev)
	}
}

// tryRegister attempts to register an event, ignoring "already bound" errors.
func tryRegister[T any](ctx context.Context, bus *event.Bus, ev event.Event[T]) error {
	err := event.Register(ctx, bus, ev)
	if err != nil && strings.Contains(err.Error(), "already bound") {
		return nil // Event already registered, that's fine
	}
	return err
}
