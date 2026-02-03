package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rbaliyan/event/v3"
)

// Event names for mailbox events.
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

// Global event instances.
//
// Deprecated: These global events use "first registration wins" semantics,
// which makes parallel testing unreliable and prevents multiple independent
// services in the same process. Prefer using Service.Events() for per-service
// event access.
var (
	// EventMessageSent is published when a message is sent.
	// Deprecated: Use Service.Events().MessageSent instead.
	EventMessageSent = event.New[MessageSentEvent](EventNameMessageSent)

	// EventMessageRead is published when a message is marked as read.
	// Deprecated: Use Service.Events().MessageRead instead.
	EventMessageRead = event.New[MessageReadEvent](EventNameMessageRead)

	// EventMessageDeleted is published when a message is permanently deleted.
	// Deprecated: Use Service.Events().MessageDeleted instead.
	EventMessageDeleted = event.New[MessageDeletedEvent](EventNameMessageDeleted)
)

// ServiceEvents provides access to per-service event instances.
// Each service creates its own events bound to its own event bus,
// enabling independent event routing and parallel testing.
//
// Subscribe to events:
//
//	svc.Events().MessageSent.Subscribe(ctx, handler)
//	svc.Events().MessageRead.Subscribe(ctx, handler)
//	svc.Events().MessageDeleted.Subscribe(ctx, handler)
type ServiceEvents struct {
	// MessageSent is published when a message is sent.
	MessageSent event.Event[MessageSentEvent]

	// MessageRead is published when a message is marked as read.
	MessageRead event.Event[MessageReadEvent]

	// MessageDeleted is published when a message is permanently deleted.
	MessageDeleted event.Event[MessageDeletedEvent]
}

// newServiceEvents creates per-service event instances with a unique name prefix.
func newServiceEvents(namePrefix string) *ServiceEvents {
	return &ServiceEvents{
		MessageSent:    event.New[MessageSentEvent](namePrefix + "." + EventNameMessageSent),
		MessageRead:    event.New[MessageReadEvent](namePrefix + "." + EventNameMessageRead),
		MessageDeleted: event.New[MessageDeletedEvent](namePrefix + "." + EventNameMessageDeleted),
	}
}

// registerServiceEvents registers per-service events with the given bus.
func registerServiceEvents(ctx context.Context, bus *event.Bus, events *ServiceEvents) error {
	if err := event.Register(ctx, bus, events.MessageSent); err != nil {
		return fmt.Errorf("register MessageSent: %w", err)
	}
	if err := event.Register(ctx, bus, events.MessageRead); err != nil {
		return fmt.Errorf("register MessageRead: %w", err)
	}
	if err := event.Register(ctx, bus, events.MessageDeleted); err != nil {
		return fmt.Errorf("register MessageDeleted: %w", err)
	}
	return nil
}

// registerEvents registers global mailbox events with the given bus.
// Global events use "first registration wins" - subsequent calls are no-ops.
//
// Deprecated: Global events are retained for backward compatibility.
// Per-service events are registered separately via registerServiceEvents.
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
		return fmt.Errorf("mailbox: unknown event type %T - update registerEvent switch", ev)
	}
}

// tryRegister attempts to register an event, ignoring "already bound" errors.
func tryRegister[T any](ctx context.Context, bus *event.Bus, ev event.Event[T]) error {
	err := event.Register(ctx, bus, ev)
	if err == nil {
		return nil
	}
	// Ignore "already bound" errors for global events that may have been
	// registered by a previous service instance.
	if errors.Is(err, event.ErrAlreadyBound) {
		return nil
	}
	return err
}
