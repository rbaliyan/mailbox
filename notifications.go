package mailbox

import (
	"context"
	"encoding/json"

	"github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/mailbox/notify"
)

// onNotifyMessageReceived handles the MessageReceived event for notifications.
// Subscribed with AsWorker — only one instance processes each event.
func (s *service) onNotifyMessageReceived(ctx context.Context, _ event.Event[MessageReceivedEvent], data MessageReceivedEvent) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.notifier.Push(ctx, data.RecipientID, notify.Event{
		Type:      EventNameMessageReceived,
		Payload:   payload,
		Timestamp: data.ReceivedAt,
	})
}

// onNotifyMessageRead handles the MessageRead event for notifications.
func (s *service) onNotifyMessageRead(ctx context.Context, _ event.Event[MessageReadEvent], data MessageReadEvent) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.notifier.Push(ctx, data.UserID, notify.Event{
		Type:      EventNameMessageRead,
		Payload:   payload,
		Timestamp: data.ReadAt,
	})
}

// onNotifyMessageDeleted handles the MessageDeleted event for notifications.
func (s *service) onNotifyMessageDeleted(ctx context.Context, _ event.Event[MessageDeletedEvent], data MessageDeletedEvent) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.notifier.Push(ctx, data.UserID, notify.Event{
		Type:      EventNameMessageDeleted,
		Payload:   payload,
		Timestamp: data.DeletedAt,
	})
}

// onNotifyMessageMoved handles the MessageMoved event for notifications.
func (s *service) onNotifyMessageMoved(ctx context.Context, _ event.Event[MessageMovedEvent], data MessageMovedEvent) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.notifier.Push(ctx, data.UserID, notify.Event{
		Type:      EventNameMessageMoved,
		Payload:   payload,
		Timestamp: data.MovedAt,
	})
}

// onNotifyMarkAllRead handles the MarkAllRead event for notifications.
func (s *service) onNotifyMarkAllRead(ctx context.Context, _ event.Event[MarkAllReadEvent], data MarkAllReadEvent) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.notifier.Push(ctx, data.UserID, notify.Event{
		Type:      EventNameMarkAllRead,
		Payload:   payload,
		Timestamp: data.MarkedAt,
	})
}

// onNotifyMessageSent handles the MessageSent event for notifications.
// Uses PushMulti for efficient batch delivery to all recipients.
func (s *service) onNotifyMessageSent(ctx context.Context, _ event.Event[MessageSentEvent], data MessageSentEvent) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	evt := notify.Event{
		Type:      EventNameMessageSent,
		Payload:   payload,
		Timestamp: data.SentAt,
	}
	failed, err := s.notifier.PushMulti(ctx, data.RecipientIDs, evt)
	if err != nil {
		return err
	}
	if failed > 0 {
		s.logger.Warn("notify: some recipient pushes failed", "failed", failed, "total", len(data.RecipientIDs))
	}
	return nil
}

// Notifications returns a notification stream for the given user.
// lastEventID enables backfill of missed events since that ID ("" for new events only).
// Returns ErrNotifierNotConfigured if no notifier was provided via WithNotifier.
// The caller must close the returned Stream when done.
func (s *service) Notifications(ctx context.Context, userID string, lastEventID string) (notify.Stream, error) {
	if !s.IsConnected() {
		return nil, ErrNotConnected
	}
	if s.notifier == nil {
		return nil, ErrNotifierNotConfigured
	}
	return s.notifier.Subscribe(ctx, userID, lastEventID)
}
