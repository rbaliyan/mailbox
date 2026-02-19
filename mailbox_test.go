package mailbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// mustCompose is a test helper that panics if Compose fails.
func mustCompose(mb Mailbox) Draft {
	d, err := mb.Compose()
	if err != nil {
		panic(err)
	}
	return d
}

func TestNewService(t *testing.T) {
	t.Run("requires store", func(t *testing.T) {
		_, err := NewService()
		if !errors.Is(err, ErrStoreRequired) {
			t.Errorf("expected ErrStoreRequired, got %v", err)
		}
	})

	t.Run("creates service with store", func(t *testing.T) {
		svc, err := NewService(WithStore(memory.New()))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svc == nil {
			t.Fatal("expected non-nil service")
		}
	})

}

func TestServiceLifecycle(t *testing.T) {
	t.Run("connect and close", func(t *testing.T) {
		svc, err := NewService(WithStore(memory.New()))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		ctx := context.Background()

		// Connect
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect failed: %v", err)
		}

		// Double connect should fail
		if err := svc.Connect(ctx); !errors.Is(err, ErrAlreadyConnected) {
			t.Errorf("expected ErrAlreadyConnected, got %v", err)
		}

		// Close
		if err := svc.Close(ctx); err != nil {
			t.Fatalf("close failed: %v", err)
		}

		// Double close should be safe
		if err := svc.Close(ctx); err != nil {
			t.Errorf("second close should not error, got %v", err)
		}
	})
}

func TestUserMailbox(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	t.Run("UserID returns correct ID", func(t *testing.T) {
		mb := svc.Client("user123")
		if mb.UserID() != "user123" {
			t.Errorf("expected UserID 'user123', got %q", mb.UserID())
		}
	})

	t.Run("operations fail when not connected", func(t *testing.T) {
		disconnectedSvc, _ := NewService(WithStore(memory.New()))
		mb := disconnectedSvc.Client("user123")

		_, err := mb.Get(ctx, "msg123")
		if !errors.Is(err, ErrNotConnected) {
			t.Errorf("expected ErrNotConnected, got %v", err)
		}

		_, err = mb.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if !errors.Is(err, ErrNotConnected) {
			t.Errorf("expected ErrNotConnected, got %v", err)
		}
	})

	t.Run("invalid user ID is rejected", func(t *testing.T) {
		mb := svc.Client("user:with:colons")
		_, err := mb.Get(ctx, "msg123")
		if !errors.Is(err, ErrInvalidUserID) {
			t.Errorf("expected ErrInvalidUserID, got %v", err)
		}
	})
}

func TestComposeSendMessage(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	sender := svc.Client("sender")
	recipient := svc.Client("recipient")

	t.Run("send message to recipient", func(t *testing.T) {
		draft := mustCompose(sender)
		draft.SetSubject("Hello").
			SetBody("This is a test message").
			SetRecipients("recipient")

		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		if msg == nil {
			t.Fatal("expected non-nil message")
		}
		if msg.GetSubject() != "Hello" {
			t.Errorf("expected subject 'Hello', got %q", msg.GetSubject())
		}
		if msg.GetBody() != "This is a test message" {
			t.Errorf("expected body 'This is a test message', got %q", msg.GetBody())
		}

		// Verify sender has message in sent folder
		sent, err := sender.Folder(ctx, store.FolderSent, store.ListOptions{})
		if err != nil {
			t.Fatalf("list sent failed: %v", err)
		}
		if len(sent.All()) != 1 {
			t.Errorf("expected 1 sent message, got %d", len(sent.All()))
		}

		// Verify recipient has message in inbox
		inbox, err := recipient.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if err != nil {
			t.Fatalf("list inbox failed: %v", err)
		}
		if len(inbox.All()) != 1 {
			t.Errorf("expected 1 inbox message, got %d", len(inbox.All()))
		}
	})

	t.Run("send to multiple recipients", func(t *testing.T) {
		r1 := svc.Client("r1")
		r2 := svc.Client("r2")

		draft := mustCompose(sender)
		draft.SetSubject("Multi").
			SetBody("Multi recipient").
			SetRecipients("r1", "r2")

		_, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		// Both should have it
		inbox1, _ := r1.Folder(ctx, store.FolderInbox, store.ListOptions{})
		inbox2, _ := r2.Folder(ctx, store.FolderInbox, store.ListOptions{})

		if len(inbox1.All()) != 1 {
			t.Errorf("r1 expected 1 message, got %d", len(inbox1.All()))
		}
		if len(inbox2.All()) != 1 {
			t.Errorf("r2 expected 1 message, got %d", len(inbox2.All()))
		}
	})

	t.Run("send with metadata", func(t *testing.T) {
		draft := mustCompose(sender)
		draft.SetSubject("With Metadata").
			SetBody("Test body").
			SetRecipients("recipient").
			SetMetadata("priority", "high").
			SetMetadata("category", "test")

		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		meta := msg.GetMetadata()
		if meta["priority"] != "high" {
			t.Errorf("expected priority 'high', got %v", meta["priority"])
		}
		if meta["category"] != "test" {
			t.Errorf("expected category 'test', got %v", meta["category"])
		}
	})

	t.Run("empty recipients fails", func(t *testing.T) {
		draft := mustCompose(sender)
		draft.SetSubject("No Recipients").
			SetBody("Test")

		_, err := draft.Send(ctx)
		if !errors.Is(err, ErrEmptyRecipients) {
			t.Errorf("expected ErrEmptyRecipients, got %v", err)
		}
	})

	t.Run("empty subject fails", func(t *testing.T) {
		draft := mustCompose(sender)
		draft.SetBody("Test").
			SetRecipients("recipient")

		_, err := draft.Send(ctx)
		if !errors.Is(err, ErrEmptySubject) {
			t.Errorf("expected ErrEmptySubject, got %v", err)
		}
	})
}

func TestSaveDraft(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	user := svc.Client("user")

	t.Run("save and retrieve draft", func(t *testing.T) {
		draft := mustCompose(user)
		draft.SetSubject("Draft Subject").
			SetBody("Draft body")

		saved, err := draft.Save(ctx)
		if err != nil {
			t.Fatalf("save failed: %v", err)
		}

		if saved.ID() == "" {
			t.Error("expected draft to have ID after save")
		}

		// List drafts
		drafts, err := user.Drafts(ctx, store.ListOptions{})
		if err != nil {
			t.Fatalf("list drafts failed: %v", err)
		}

		if len(drafts.All()) != 1 {
			t.Errorf("expected 1 draft, got %d", len(drafts.All()))
		}
		if drafts.All()[0].Subject() != "Draft Subject" {
			t.Errorf("expected subject 'Draft Subject', got %q", drafts.All()[0].Subject())
		}
	})

	t.Run("delete draft", func(t *testing.T) {
		draft := mustCompose(user)
		draft.SetSubject("To Delete").
			SetBody("Body")

		saved, _ := draft.Save(ctx)
		err := saved.Delete(ctx)
		if err != nil {
			t.Fatalf("delete failed: %v", err)
		}

		drafts, _ := user.Drafts(ctx, store.ListOptions{})
		for _, d := range drafts.All() {
			if d.Subject() == "To Delete" {
				t.Error("draft should have been deleted")
			}
		}
	})
}

func TestMessageOperations(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	sender := svc.Client("sender")
	recipient := svc.Client("recipient")

	// Send a message first
	draft := mustCompose(sender)
	draft.SetSubject("Test Message").
		SetBody("Test body").
		SetRecipients("recipient")
	_, _ = draft.Send(ctx)

	// Get recipient's inbox
	inbox, _ := recipient.Folder(ctx, store.FolderInbox, store.ListOptions{})
	if len(inbox.All()) == 0 {
		t.Fatal("expected message in inbox")
	}
	msg := inbox.All()[0]

	t.Run("mark read", func(t *testing.T) {
		err := msg.Update(ctx, MarkRead())
		if err != nil {
			t.Fatalf("mark read failed: %v", err)
		}

		// Verify
		updated, _ := recipient.Get(ctx, msg.GetID())
		if !updated.GetIsRead() {
			t.Error("message should be marked as read")
		}
	})

	t.Run("mark unread", func(t *testing.T) {
		err := msg.Update(ctx, MarkUnread())
		if err != nil {
			t.Fatalf("mark unread failed: %v", err)
		}

		updated, _ := recipient.Get(ctx, msg.GetID())
		if updated.GetIsRead() {
			t.Error("message should be marked as unread")
		}
	})

	t.Run("archive message", func(t *testing.T) {
		err := msg.Move(ctx, store.FolderArchived)
		if err != nil {
			t.Fatalf("archive failed: %v", err)
		}

		// Should be in archived folder
		archived, _ := recipient.Folder(ctx, store.FolderArchived, store.ListOptions{})
		found := false
		for _, m := range archived.All() {
			if m.GetID() == msg.GetID() {
				found = true
				break
			}
		}
		if !found {
			t.Error("message should be in archived folder")
		}
	})

	t.Run("delete and restore", func(t *testing.T) {
		// First unarchive to move back
		updated, _ := recipient.Get(ctx, msg.GetID())

		err := updated.Delete(ctx)
		if err != nil {
			t.Fatalf("delete failed: %v", err)
		}

		// Should be in trash
		trash, _ := recipient.Folder(ctx, store.FolderTrash, store.ListOptions{})
		found := false
		for _, m := range trash.All() {
			if m.GetID() == msg.GetID() {
				found = true
				break
			}
		}
		if !found {
			t.Error("message should be in trash")
		}

		// Restore
		trashedMsg := trash.All()[0]
		err = trashedMsg.Restore(ctx)
		if err != nil {
			t.Fatalf("restore failed: %v", err)
		}

		// Should not be in trash anymore
		trash, _ = recipient.Folder(ctx, store.FolderTrash, store.ListOptions{})
		if len(trash.All()) > 0 {
			t.Error("trash should be empty after restore")
		}
	})
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	sender := svc.Client("sender")
	recipient := svc.Client("recipient")

	// Send some messages
	subjects := []string{"Hello World", "Test Message", "Hello Again", "Goodbye"}
	for _, subject := range subjects {
		draft := mustCompose(sender)
		draft.SetSubject(subject).
			SetBody("Body for " + subject).
			SetRecipients("recipient")
		_, _ = draft.Send(ctx)
	}

	t.Run("search by subject", func(t *testing.T) {
		results, err := recipient.Search(ctx, store.SearchQuery{
			Query: "Hello",
		})
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}

		// Should find "Hello World" and "Hello Again"
		if len(results.All()) != 2 {
			t.Errorf("expected 2 results, got %d", len(results.All()))
		}
	})

	t.Run("search with no results", func(t *testing.T) {
		results, err := recipient.Search(ctx, store.SearchQuery{
			Query: "NonexistentText",
		})
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}

		if len(results.All()) != 0 {
			t.Errorf("expected 0 results, got %d", len(results.All()))
		}
	})
}

func TestTags(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	sender := svc.Client("sender")
	recipient := svc.Client("recipient")

	// Send a message
	draft := mustCompose(sender)
	draft.SetSubject("Tagged Message").
		SetBody("Body").
		SetRecipients("recipient")
	_, _ = draft.Send(ctx)

	inbox, _ := recipient.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]

	t.Run("add and remove tags", func(t *testing.T) {
		err := msg.AddTag(ctx, "important")
		if err != nil {
			t.Fatalf("add tag failed: %v", err)
		}

		err = msg.AddTag(ctx, "work")
		if err != nil {
			t.Fatalf("add tag failed: %v", err)
		}

		// Verify tags
		updated, _ := recipient.Get(ctx, msg.GetID())
		tags := updated.GetTags()
		if len(tags) != 2 {
			t.Errorf("expected 2 tags, got %d", len(tags))
		}

		// Remove tag
		err = updated.RemoveTag(ctx, "work")
		if err != nil {
			t.Fatalf("remove tag failed: %v", err)
		}

		updated, _ = recipient.Get(ctx, msg.GetID())
		if len(updated.GetTags()) != 1 {
			t.Errorf("expected 1 tag after removal, got %d", len(updated.GetTags()))
		}
	})
}

func TestSendWithHeaders(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	sender := svc.Client("sender")
	recipient := svc.Client("recipient")

	t.Run("send message with headers", func(t *testing.T) {
		draft := mustCompose(sender)
		draft.SetSubject("Structured Message").
			SetBody(`{"temperature":72}`).
			SetRecipients("recipient").
			SetHeader(store.HeaderContentType, "application/json").
			SetHeader(store.HeaderSchema, "sensor.reading/v1").
			SetHeader(store.HeaderPriority, "high")

		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		// Verify sender copy has headers
		headers := msg.GetHeaders()
		if headers[store.HeaderContentType] != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", headers[store.HeaderContentType])
		}
		if headers[store.HeaderSchema] != "sensor.reading/v1" {
			t.Errorf("Schema = %q, want sensor.reading/v1", headers[store.HeaderSchema])
		}
		if headers[store.HeaderPriority] != "high" {
			t.Errorf("Priority = %q, want high", headers[store.HeaderPriority])
		}

		// Verify recipient copy has headers
		inbox, _ := recipient.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(inbox.All()) == 0 {
			t.Fatal("expected message in recipient inbox")
		}
		recipientHeaders := inbox.All()[0].GetHeaders()
		if recipientHeaders[store.HeaderContentType] != "application/json" {
			t.Errorf("recipient Content-Type = %q, want application/json", recipientHeaders[store.HeaderContentType])
		}
		if recipientHeaders[store.HeaderSchema] != "sensor.reading/v1" {
			t.Errorf("recipient Schema = %q, want sensor.reading/v1", recipientHeaders[store.HeaderSchema])
		}
	})

	t.Run("Content-Length auto-populated on send", func(t *testing.T) {
		draft := mustCompose(sender)
		body := "Hello, World!"
		draft.SetSubject("Auto Length").
			SetBody(body).
			SetRecipients("recipient")

		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		headers := msg.GetHeaders()
		expected := "13" // len("Hello, World!") == 13 bytes
		if headers[store.HeaderContentLength] != expected {
			t.Errorf("Content-Length = %q, want %q", headers[store.HeaderContentLength], expected)
		}
	})

	t.Run("Content-Length not overwritten if already set", func(t *testing.T) {
		draft := mustCompose(sender)
		draft.SetSubject("Custom Length").
			SetBody("Body").
			SetRecipients("recipient").
			SetHeader(store.HeaderContentLength, "999")

		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		if msg.GetHeaders()[store.HeaderContentLength] != "999" {
			t.Errorf("Content-Length = %q, want 999 (should not be overwritten)", msg.GetHeaders()[store.HeaderContentLength])
		}
	})

	t.Run("send without headers has only Content-Length", func(t *testing.T) {
		draft := mustCompose(sender)
		draft.SetSubject("No Headers").
			SetBody("Plain text").
			SetRecipients("recipient")

		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		headers := msg.GetHeaders()
		if headers[store.HeaderContentLength] == "" {
			t.Error("Content-Length should be auto-populated")
		}
		if headers[store.HeaderContentType] != "" {
			t.Errorf("Content-Type should not be set, got %q", headers[store.HeaderContentType])
		}
	})
}

func TestSaveDraftWithHeaders(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	user := svc.Client("user")

	t.Run("save and retrieve draft with headers", func(t *testing.T) {
		draft := mustCompose(user)
		draft.SetSubject("Draft with Headers").
			SetBody("Body").
			SetHeader(store.HeaderContentType, "application/json").
			SetHeader(store.HeaderSchema, "order/v1")

		saved, err := draft.Save(ctx)
		if err != nil {
			t.Fatalf("save failed: %v", err)
		}

		// Retrieve draft and verify headers persisted
		drafts, err := user.Drafts(ctx, store.ListOptions{})
		if err != nil {
			t.Fatalf("list drafts failed: %v", err)
		}

		found := false
		for _, d := range drafts.All() {
			if d.Subject() == "Draft with Headers" {
				found = true
				headers := d.Headers()
				if headers[store.HeaderContentType] != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", headers[store.HeaderContentType])
				}
				if headers[store.HeaderSchema] != "order/v1" {
					t.Errorf("Schema = %q, want order/v1", headers[store.HeaderSchema])
				}
				break
			}
		}
		if !found {
			t.Error("draft not found in list")
		}

		// Clean up
		_ = saved.Delete(ctx)
	})
}

func TestSendMessageWithHeaders(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	mb := svc.Client("sender")

	t.Run("SendMessage copies headers from request", func(t *testing.T) {
		msg, err := mb.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"recipient"},
			Subject:      "Via SendRequest",
			Body:         "Body",
			Headers: map[string]string{
				store.HeaderContentType:  "text/plain",
				store.HeaderCorrelationID: "corr-123",
			},
		})
		if err != nil {
			t.Fatalf("SendMessage failed: %v", err)
		}

		headers := msg.GetHeaders()
		if headers[store.HeaderContentType] != "text/plain" {
			t.Errorf("Content-Type = %q, want text/plain", headers[store.HeaderContentType])
		}
		if headers[store.HeaderCorrelationID] != "corr-123" {
			t.Errorf("Correlation-ID = %q, want corr-123", headers[store.HeaderCorrelationID])
		}
	})
}

func TestUnauthorizedAccess(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	sender := svc.Client("sender")
	attacker := svc.Client("attacker")

	// Send a message
	draft := mustCompose(sender)
	draft.SetSubject("Private").
		SetBody("Secret content").
		SetRecipients("recipient")
	msg, _ := draft.Send(ctx)

	t.Run("cannot access other user's message", func(t *testing.T) {
		_, err := attacker.Get(ctx, msg.GetID())
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("expected ErrUnauthorized, got %v", err)
		}
	})
}

func TestConcurrentSends(t *testing.T) {
	ctx := context.Background()
	svc := setupTestService(t)
	defer svc.Close(ctx)

	sender := svc.Client("sender")

	var wg sync.WaitGroup
	errChan := make(chan error, 20)

	// Send 20 messages concurrently
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			draft := mustCompose(sender)
			draft.SetSubject("Concurrent Message").
				SetBody("Body").
				SetRecipients("recipient")

			_, err := draft.Send(ctx)
			if err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		t.Errorf("concurrent send error: %v", err)
	}

	// Verify all messages were sent
	sent, _ := sender.Folder(ctx, store.FolderSent, store.ListOptions{Limit: 100})
	if len(sent.All()) != 20 {
		t.Errorf("expected 20 sent messages, got %d", len(sent.All()))
	}
}

func TestGracefulShutdown(t *testing.T) {
	ctx := context.Background()
	svc, _ := NewService(WithStore(memory.New()))
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	sender := svc.Client("sender")

	// Compose draft before starting goroutine to avoid race with Close
	draft := mustCompose(sender)
	draft.SetSubject("During Shutdown").
		SetBody("Body").
		SetRecipients("recipient")

	// Start a send
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = draft.Send(ctx)
	}()

	// Give it a moment to start
	time.Sleep(10 * time.Millisecond)

	// Close should wait for in-flight operations
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := svc.Close(closeCtx)
	if err != nil {
		t.Errorf("close returned error: %v", err)
	}

	wg.Wait()
}

// Helper to setup a test service
func setupTestService(t *testing.T) Service {
	t.Helper()

	svc, err := NewService(
		WithStore(memory.New()),
	)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	ctx := context.Background()
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	return svc
}
