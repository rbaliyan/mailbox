package mailbox

import (
	"context"
	"sync"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func TestConcurrency_MultipleSenders(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc, err := NewService(WithStore(memStore))
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer svc.Close(ctx)

	const numSenders = 10
	const messagesPerSender = 5

	var wg sync.WaitGroup
	errors := make(chan error, numSenders*messagesPerSender)

	// Multiple users sending messages concurrently
	for i := 0; i < numSenders; i++ {
		wg.Add(1)
		go func(senderNum int) {
			defer wg.Done()
			userID := "sender" + string(rune('0'+senderNum))
			client := svc.Client(userID)

			for j := 0; j < messagesPerSender; j++ {
				draft := mustCompose(client)
				draft.SetRecipients("recipient1", "recipient2").
					SetSubject("Concurrent test message").
					SetBody("Test body")

				_, err := draft.Send(ctx)
				if err != nil {
					errors <- err
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	var errCount int
	for err := range errors {
		t.Errorf("send error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("%d errors occurred during concurrent sends", errCount)
	}
}

func TestConcurrentReads(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc, err := NewService(WithStore(memStore))
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer svc.Close(ctx)

	// Create some messages first
	sender := svc.Client("sender")
	for i := 0; i < 10; i++ {
		draft := mustCompose(sender)
		draft.SetRecipients("reader").
			SetSubject("Test message").
			SetBody("Body")
		_, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("failed to send message: %v", err)
		}
	}

	reader := svc.Client("reader")

	// Concurrent reads
	const numReaders = 20
	var wg sync.WaitGroup
	errors := make(chan error, numReaders*3)

	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Read inbox
			inbox, err := reader.Inbox(ctx, store.ListOptions{Limit: 10})
			if err != nil {
				errors <- err
				return
			}

			// Read each message
			for _, msg := range inbox.All() {
				_, err := reader.Get(ctx, msg.GetID())
				if err != nil {
					errors <- err
				}
			}
		}()
	}

	wg.Wait()
	close(errors)

	var errCount int
	for err := range errors {
		t.Errorf("read error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("%d errors occurred during concurrent reads", errCount)
	}
}

func TestConcurrentMarkRead(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc, err := NewService(WithStore(memStore))
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer svc.Close(ctx)

	// Send a message
	sender := svc.Client("sender")
	draft := mustCompose(sender)
	draft.SetRecipients("reader").
		SetSubject("Test").
		SetBody("Body")
	_, err = draft.Send(ctx)
	if err != nil {
		t.Fatalf("failed to send: %v", err)
	}

	reader := svc.Client("reader")
	inbox, err := reader.Inbox(ctx, store.ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("failed to get inbox: %v", err)
	}
	if len(inbox.All()) == 0 {
		t.Fatal("no messages in inbox")
	}

	msg := inbox.All()[0]

	// Concurrent mark read/unread toggles
	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var err error
			if n%2 == 0 {
				err = msg.Update(ctx, MarkRead())
			} else {
				err = msg.Update(ctx, MarkUnread())
			}
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errCount int
	for err := range errors {
		t.Errorf("update error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("%d errors occurred during concurrent updates", errCount)
	}
}

func TestConcurrentServiceAccess(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc, err := NewService(WithStore(memStore))
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer svc.Close(ctx)

	// Multiple goroutines creating clients and performing operations
	const numGoroutines = 50
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*2)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			userID := "user" + string(rune('a'+n%26))
			client := svc.Client(userID)

			// Send
			draft := mustCompose(client)
			draft.SetRecipients("recipient").
				SetSubject("Test").
				SetBody("Body")
			_, err := draft.Send(ctx)
			if err != nil {
				errors <- err
				return
			}

			// Read sent
			_, err = client.Sent(ctx, store.ListOptions{Limit: 10})
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errCount int
	for err := range errors {
		t.Errorf("operation error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("%d errors occurred during concurrent service access", errCount)
	}
}

func TestConcurrentDraftOperations(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc, err := NewService(WithStore(memStore))
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer svc.Close(ctx)

	client := svc.Client("drafter")

	// Create multiple drafts concurrently
	const numDrafts = 10
	var wg sync.WaitGroup
	drafts := make(chan Draft, numDrafts)
	errors := make(chan error, numDrafts)

	for i := 0; i < numDrafts; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			draft := mustCompose(client)
			draft.SetRecipients("recipient").
				SetSubject("Draft " + string(rune('0'+n))).
				SetBody("Draft body")

			savedDraft, err := draft.Save(ctx)
			if err != nil {
				errors <- err
				return
			}
			drafts <- savedDraft
		}(i)
	}

	wg.Wait()
	close(drafts)
	close(errors)

	var errCount int
	for err := range errors {
		t.Errorf("draft save error: %v", err)
		errCount++
	}

	// Count saved drafts
	draftCount := 0
	for range drafts {
		draftCount++
	}

	if errCount > 0 {
		t.Errorf("%d errors occurred during concurrent draft saves", errCount)
	}
	if draftCount != numDrafts {
		t.Errorf("expected %d drafts, got %d", numDrafts, draftCount)
	}
}

func TestConcurrentFolderMoves(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	svc, err := NewService(WithStore(memStore))
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer svc.Close(ctx)

	// Send multiple messages
	sender := svc.Client("sender")
	reader := svc.Client("reader")

	for i := 0; i < 5; i++ {
		draft := mustCompose(sender)
		draft.SetRecipients("reader").
			SetSubject("Test").
			SetBody("Body")
		_, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("failed to send: %v", err)
		}
	}

	inbox, err := reader.Inbox(ctx, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("failed to get inbox: %v", err)
	}

	messages := inbox.All()
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(messages))
	}

	// Concurrently move messages to different folders
	var wg sync.WaitGroup
	errors := make(chan error, len(messages)*2)
	folders := []string{store.FolderArchived, "custom-folder", store.FolderInbox}

	for _, msg := range messages {
		for _, folder := range folders {
			wg.Add(1)
			go func(m Message, f string) {
				defer wg.Done()
				if err := m.Move(ctx, f); err != nil {
					errors <- err
				}
			}(msg, folder)
		}
	}

	wg.Wait()
	close(errors)

	var errCount int
	for err := range errors {
		t.Errorf("move error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("%d errors occurred during concurrent folder moves", errCount)
	}
}
