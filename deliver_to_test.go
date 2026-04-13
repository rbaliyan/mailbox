package mailbox

import (
	"context"
	"errors"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func TestDeliverTo(t *testing.T) {
	ctx := context.Background()

	t.Run("empty DeliverTo delivers to all RecipientIDs", func(t *testing.T) {
		svc := setupTestService(t)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		bob := svc.Client("bob")
		charlie := svc.Client("charlie")

		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob", "charlie"},
			Subject:      "Hello everyone",
			Body:         "Test",
		})
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		// Both recipients should have the message
		bobInbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(bobInbox.All()) != 1 {
			t.Errorf("bob: expected 1 inbox message, got %d", len(bobInbox.All()))
		}
		charlieInbox, _ := charlie.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(charlieInbox.All()) != 1 {
			t.Errorf("charlie: expected 1 inbox message, got %d", len(charlieInbox.All()))
		}
	})

	t.Run("DeliverTo restricts delivery to subset", func(t *testing.T) {
		svc := setupTestService(t)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		bob := svc.Client("bob")
		charlie := svc.Client("charlie")

		msg, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob", "charlie"},
			DeliverTo:    []string{"bob"}, // only deliver to bob
			Subject:      "Selective delivery",
			Body:         "Test",
		})
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		// Bob should have the message
		bobInbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(bobInbox.All()) != 1 {
			t.Fatalf("bob: expected 1 inbox message, got %d", len(bobInbox.All()))
		}

		// Charlie should NOT have the message (not in DeliverTo)
		charlieInbox, _ := charlie.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(charlieInbox.All()) != 0 {
			t.Errorf("charlie: expected 0 inbox messages, got %d", len(charlieInbox.All()))
		}

		// Sender's copy should list all recipients
		sentList, _ := alice.Folder(ctx, store.FolderSent, store.ListOptions{})
		if len(sentList.All()) != 1 {
			t.Fatalf("alice: expected 1 sent message, got %d", len(sentList.All()))
		}
		senderRecipients := msg.GetRecipientIDs()
		if len(senderRecipients) != 2 {
			t.Errorf("sender copy: expected 2 recipients, got %d", len(senderRecipients))
		}

		// Bob's copy should also list all recipients
		bobMsg := bobInbox.All()[0]
		bobRecipients := bobMsg.GetRecipientIDs()
		if len(bobRecipients) != 2 {
			t.Errorf("bob copy: expected 2 recipients, got %d: %v", len(bobRecipients), bobRecipients)
		}
	})

	t.Run("DeliverTo via DraftComposer", func(t *testing.T) {
		svc := setupTestService(t)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		bob := svc.Client("bob")
		charlie := svc.Client("charlie")

		draft := mustCompose(alice)
		draft.SetRecipients("bob", "charlie").
			SetDeliverTo("charlie"). // only deliver to charlie
			SetSubject("Via draft").
			SetBody("Test")

		_, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		// Bob should NOT have the message
		bobInbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(bobInbox.All()) != 0 {
			t.Errorf("bob: expected 0 inbox messages, got %d", len(bobInbox.All()))
		}

		// Charlie should have it
		charlieInbox, _ := charlie.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(charlieInbox.All()) != 1 {
			t.Errorf("charlie: expected 1 inbox message, got %d", len(charlieInbox.All()))
		}
	})

	t.Run("DeliverTo deduplicates", func(t *testing.T) {
		svc := setupTestService(t)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		bob := svc.Client("bob")

		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			DeliverTo:    []string{"bob", "bob"}, // duplicate
			Subject:      "Dedup test",
			Body:         "Test",
		})
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		// Bob should have exactly 1 message
		bobInbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(bobInbox.All()) != 1 {
			t.Errorf("bob: expected 1 inbox message, got %d", len(bobInbox.All()))
		}
	})

	t.Run("DeliverTo with partial delivery failure", func(t *testing.T) {
		memStore := memory.New()
		svc, err := New(DefaultConfig(),
			WithStore(memStore),
			WithGlobalQuota(QuotaPolicy{
				MaxMessages:  1,
				ExceedAction: QuotaActionReject,
			}),
		)
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		bob := svc.Client("bob")

		// Fill bob's quota
		_, err = alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Fill quota",
			Body:         "First",
		})
		if err != nil {
			t.Fatalf("first send: %v", err)
		}

		// Now send to bob and charlie with DeliverTo
		charlie := svc.Client("charlie")
		_, err = alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob", "charlie"},
			DeliverTo:    []string{"bob", "charlie"},
			Subject:      "Partial",
			Body:         "Test",
		})

		// Bob should fail (quota), charlie should succeed
		if err == nil {
			t.Fatal("expected partial delivery error")
		}
		pde, ok := IsPartialDelivery(err)
		if !ok {
			t.Fatalf("expected PartialDeliveryError, got %T: %v", err, err)
		}
		if len(pde.DeliveredTo) != 1 || pde.DeliveredTo[0] != "charlie" {
			t.Errorf("expected delivered to [charlie], got %v", pde.DeliveredTo)
		}
		if _, failed := pde.FailedRecipients["bob"]; !failed {
			t.Errorf("expected bob in failed recipients, got %v", pde.FailedRecipients)
		}

		// Charlie should have the message
		charlieInbox, _ := charlie.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(charlieInbox.All()) != 1 {
			t.Errorf("charlie: expected 1 inbox message, got %d", len(charlieInbox.All()))
		}

		// Bob should have only the first message
		bobInbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(bobInbox.All()) != 1 {
			t.Errorf("bob: expected 1 inbox message (from first send), got %d", len(bobInbox.All()))
		}
	})

	t.Run("DraftReader DeliverTo returns set values", func(t *testing.T) {
		svc := setupTestService(t)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		draft := mustCompose(alice)
		draft.SetRecipients("bob", "charlie").
			SetDeliverTo("bob")

		got := draft.DeliverTo()
		if len(got) != 1 || got[0] != "bob" {
			t.Errorf("DeliverTo() = %v, want [bob]", got)
		}
	})

	t.Run("DraftReader DeliverTo returns nil when not set", func(t *testing.T) {
		svc := setupTestService(t)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		draft := mustCompose(alice)
		draft.SetRecipients("bob")

		got := draft.DeliverTo()
		if got != nil {
			t.Errorf("DeliverTo() = %v, want nil", got)
		}
	})

	t.Run("DeliverTo with ID not in RecipientIDs is rejected", func(t *testing.T) {
		svc := setupTestService(t)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		bob := svc.Client("bob")
		charlie := svc.Client("charlie")

		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			DeliverTo:    []string{"charlie"}, // not in RecipientIDs
			Subject:      "Ghost",
			Body:         "Test",
		})
		if err == nil {
			t.Fatal("expected error when DeliverTo contains ID not in RecipientIDs")
		}
		if !errors.Is(err, ErrInvalidRecipient) {
			t.Errorf("expected ErrInvalidRecipient, got %v", err)
		}

		// No inbox copies should have been created
		bobInbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(bobInbox.All()) != 0 {
			t.Errorf("bob: expected 0 inbox messages, got %d", len(bobInbox.All()))
		}
		charlieInbox, _ := charlie.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(charlieInbox.All()) != 0 {
			t.Errorf("charlie: expected 0 inbox messages, got %d", len(charlieInbox.All()))
		}
	})

	t.Run("DeliverTo partially out-of-recipients is rejected", func(t *testing.T) {
		svc := setupTestService(t)
		defer svc.Close(ctx)

		alice := svc.Client("alice")

		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			DeliverTo:    []string{"bob", "charlie"}, // charlie not in RecipientIDs
			Subject:      "Partial ghost",
			Body:         "Test",
		})
		if err == nil {
			t.Fatal("expected error when any DeliverTo ID is not in RecipientIDs")
		}
		if !errors.Is(err, ErrInvalidRecipient) {
			t.Errorf("expected ErrInvalidRecipient, got %v", err)
		}
	})
}
