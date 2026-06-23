package mailbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/notify"
	notifymem "github.com/rbaliyan/mailbox/notify/memory"
	"github.com/rbaliyan/mailbox/store"
)

// newNotifierService wires a service with an in-memory notify store so the
// event-to-notification bridge handlers (notifications.go) run.
func newNotifierService(t *testing.T) mailbox.Service {
	t.Helper()
	notifier := notify.NewNotifier(
		notify.WithStore(notifymem.New()),
		notify.WithPollInterval(20*time.Millisecond),
	)
	return mailboxtest.NewService(t, mailbox.Config{}, mailbox.WithNotifier(notifier))
}

// waitForEvent blocks on stream.Next until an event of the given type arrives
// for the expected user, or the deadline elapses.
func waitForEvent(t *testing.T, stream notify.Stream, wantType, wantUser string) {
	t.Helper()
	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		evt, err := stream.Next(readCtx)
		if err != nil {
			t.Fatalf("stream.Next waiting for %s: %v", wantType, err)
		}
		if evt.Type == wantType {
			if evt.UserID != wantUser {
				t.Fatalf("%s event UserID = %q, want %q", wantType, evt.UserID, wantUser)
			}
			return
		}
	}
}

// deliverToBob sends one message and returns bob's inbox copy ID.
func deliverToBob(t *testing.T, svc mailbox.Service) (bob mailbox.Mailbox, msgID string) {
	t.Helper()
	ctx := context.Background()
	alice := svc.Client("alice")
	bob = svc.Client("bob")
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "hello",
		Body:         "body",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	var id string
	mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
		list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 10})
		if err != nil || len(list.All()) == 0 {
			return false
		}
		id = list.All()[0].GetID()
		return true
	}, "bob inbox copy never arrived")
	return bob, id
}

func TestNotificationBridge_MessageRead(t *testing.T) {
	ctx := context.Background()
	svc := newNotifierService(t)
	bob, msgID := deliverToBob(t, svc)

	stream, err := svc.Notifications(ctx, "bob", "")
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	defer stream.Close()

	if err := bob.UpdateFlags(ctx, msgID, mailbox.MarkRead()); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	waitForEvent(t, stream, mailbox.EventNameMessageRead, "bob")
}

func TestNotificationBridge_MessageMoved(t *testing.T) {
	ctx := context.Background()
	svc := newNotifierService(t)
	bob, msgID := deliverToBob(t, svc)

	stream, err := svc.Notifications(ctx, "bob", "")
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	defer stream.Close()

	if err := bob.MoveToFolder(ctx, msgID, store.FolderArchived); err != nil {
		t.Fatalf("move: %v", err)
	}
	waitForEvent(t, stream, mailbox.EventNameMessageMoved, "bob")
}

func TestNotificationBridge_MessageDeleted(t *testing.T) {
	ctx := context.Background()
	svc := newNotifierService(t)
	bob, msgID := deliverToBob(t, svc)

	stream, err := svc.Notifications(ctx, "bob", "")
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	defer stream.Close()

	// PermanentlyDelete publishes the MessageDeleted event; soft Delete emits
	// a move-to-trash. Move to trash first, then permanently delete.
	if err := bob.Delete(ctx, msgID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if err := bob.PermanentlyDelete(ctx, msgID); err != nil {
		t.Fatalf("permanently delete: %v", err)
	}
	waitForEvent(t, stream, mailbox.EventNameMessageDeleted, "bob")
}

func TestNotificationBridge_MarkAllRead(t *testing.T) {
	ctx := context.Background()
	svc := newNotifierService(t)
	bob, _ := deliverToBob(t, svc)

	stream, err := svc.Notifications(ctx, "bob", "")
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	defer stream.Close()

	if _, err := bob.MarkAllRead(ctx, store.FolderInbox); err != nil {
		t.Fatalf("mark all read: %v", err)
	}
	waitForEvent(t, stream, mailbox.EventNameMarkAllRead, "bob")
}
