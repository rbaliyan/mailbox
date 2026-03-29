package mailbox_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"time"

	"github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event/v3/transport/channel"
	"crypto/rand"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/compress"
	"github.com/rbaliyan/mailbox/crypto"
	"github.com/rbaliyan/mailbox/notify"
	notifymem "github.com/rbaliyan/mailbox/notify/memory"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
	"golang.org/x/crypto/curve25519"
)

// newTestService creates a connected service for examples using in-memory backends.
// Channel transport enables synchronous event delivery without Redis.
func newTestService() mailbox.Service {
	svc, err := mailbox.NewService(
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
	)
	if err != nil {
		log.Fatal(err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		log.Fatal(err)
	}
	return svc
}

// newTestServiceWithNotifications creates a connected service with notification support.
func newTestServiceWithNotifications() mailbox.Service {
	notifier := notify.NewNotifier(
		notify.WithStore(notifymem.New()),
		notify.WithPollInterval(50*time.Millisecond),
	)
	svc, err := mailbox.NewService(
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithNotifier(notifier),
	)
	if err != nil {
		log.Fatal(err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		log.Fatal(err)
	}
	return svc
}

// ExampleNewService demonstrates creating and connecting a mailbox service.
func ExampleNewService() {
	svc, err := mailbox.NewService(
		mailbox.WithStore(memory.New()),
		// Production: use mailbox.WithRedisClient(redisClient) for events
		// Production: use mongostore.New(mongoClient) for storage
	)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := svc.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer svc.Close(ctx)

	fmt.Println("connected:", svc.IsConnected())
	// Output:
	// connected: true
}

// ExampleNewService_withOptions demonstrates all major configuration options.
func ExampleNewService_withOptions() {
	svc, err := mailbox.NewService(
		mailbox.WithStore(memory.New()),

		// Message limits
		mailbox.WithMaxBodySize(5*1024*1024),     // 5 MB
		mailbox.WithMaxSubjectLength(200),         // 200 chars
		mailbox.WithMaxRecipients(50),             // 50 recipients
		mailbox.WithMaxAttachmentCount(10),        // 10 attachments
		mailbox.WithMaxAttachmentSize(50*1024*1024), // 50 MB per attachment

		// Query limits
		mailbox.WithMaxQueryLimit(200),
		mailbox.WithDefaultQueryLimit(25),

		// Trash and retention
		mailbox.WithTrashRetention(7*24*time.Hour),      // 7 days
		mailbox.WithMessageRetention(90*24*time.Hour),    // 90 days

		// Concurrency
		mailbox.WithMaxConcurrentSends(20),
		mailbox.WithShutdownTimeout(60*time.Second),
	)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := svc.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer svc.Close(ctx)

	fmt.Println("connected:", svc.IsConnected())
	// Output:
	// connected: true
}

// ExampleMailbox_SendMessage demonstrates sending a message directly.
func ExampleMailbox_SendMessage() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	msg, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob", "charlie"},
		Subject:      "Team sync",
		Body:         "Let's meet at 3pm.",
		Headers:      map[string]string{"Content-Type": "text/plain"},
		Metadata:     map[string]any{"priority": "high"},
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("sent:", msg.GetSubject())
	fmt.Println("to:", msg.GetRecipientIDs())

	// Bob sees the message in his inbox.
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	fmt.Println("bob inbox:", len(inbox.All()))
	// Output:
	// sent: Team sync
	// to: [bob charlie]
	// bob inbox: 1
}

// ExampleMailbox_Compose demonstrates the draft workflow: compose, save, edit, send.
func ExampleMailbox_Compose() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")

	// Step 1: Compose a draft.
	draft, err := alice.Compose()
	if err != nil {
		log.Fatal(err)
	}
	draft.SetSubject("Draft message").
		SetBody("Work in progress...").
		SetRecipients("bob")

	// Step 2: Save the draft for later.
	saved, err := draft.Save(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("draft saved:", saved.Subject())

	// Step 3: List drafts.
	drafts, _ := alice.Drafts(ctx, store.ListOptions{})
	fmt.Println("drafts:", len(drafts.All()))

	// Step 4: Edit and send the draft.
	saved.SetBody("Final version.")
	msg, err := saved.Send(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("sent:", msg.GetSubject())

	// Draft is gone after sending.
	drafts, _ = alice.Drafts(ctx, store.ListOptions{})
	fmt.Println("drafts after send:", len(drafts.All()))
	// Output:
	// draft saved: Draft message
	// drafts: 1
	// sent: Draft message
	// drafts after send: 0
}

// ExampleMailbox_Folder demonstrates listing messages in different folders.
func ExampleMailbox_Folder() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")

	// Send 3 messages to bob.
	for i := 1; i <= 3; i++ {
		alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      fmt.Sprintf("Message %d", i),
			Body:         "body",
		})
	}

	bob := svc.Client("bob")

	// List inbox with pagination.
	page1, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 2})
	fmt.Println("page 1:", len(page1.All()), "has_more:", page1.HasMore())

	// Alice's sent folder.
	sent, _ := alice.Folder(ctx, store.FolderSent, store.ListOptions{})
	fmt.Println("alice sent:", len(sent.All()))
	// Output:
	// page 1: 2 has_more: true
	// alice sent: 3
}

// ExampleMailbox_UpdateFlags demonstrates marking messages as read/unread.
func ExampleMailbox_UpdateFlags() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Important",
		Body:         "Read this!",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]
	fmt.Println("is_read:", msg.GetIsRead())

	// Mark as read.
	bob.UpdateFlags(ctx, msg.GetID(), mailbox.MarkRead())
	updated, _ := bob.Get(ctx, msg.GetID())
	fmt.Println("after mark read:", updated.GetIsRead())

	// Mark as unread.
	bob.UpdateFlags(ctx, msg.GetID(), mailbox.MarkUnread())
	updated, _ = bob.Get(ctx, msg.GetID())
	fmt.Println("after mark unread:", updated.GetIsRead())

	// Mark all read in inbox.
	count, _ := bob.MarkAllRead(ctx, store.FolderInbox)
	fmt.Println("marked read:", count)
	// Output:
	// is_read: false
	// after mark read: true
	// after mark unread: false
	// marked read: 1
}

// ExampleMailbox_Delete demonstrates soft delete, restore, and permanent delete.
func ExampleMailbox_Delete() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Delete me",
		Body:         "body",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msgID := inbox.All()[0].GetID()

	// Soft delete — moves to trash.
	bob.Delete(ctx, msgID)
	inbox, _ = bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	trash, _ := bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	fmt.Println("inbox:", len(inbox.All()), "trash:", len(trash.All()))

	// Restore from trash.
	bob.Restore(ctx, msgID)
	inbox, _ = bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	trash, _ = bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	fmt.Println("after restore - inbox:", len(inbox.All()), "trash:", len(trash.All()))

	// Permanent delete (must be in trash first).
	bob.Delete(ctx, msgID)
	bob.PermanentlyDelete(ctx, msgID)
	trash, _ = bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	fmt.Println("after permanent delete - trash:", len(trash.All()))
	// Output:
	// inbox: 0 trash: 1
	// after restore - inbox: 1 trash: 0
	// after permanent delete - trash: 0
}

// ExampleMailbox_MoveToFolder demonstrates moving messages between folders.
func ExampleMailbox_MoveToFolder() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Archive this",
		Body:         "body",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msgID := inbox.All()[0].GetID()

	// Move to archive.
	bob.MoveToFolder(ctx, msgID, store.FolderArchived)
	inbox, _ = bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	archived, _ := bob.Folder(ctx, store.FolderArchived, store.ListOptions{})
	fmt.Println("inbox:", len(inbox.All()), "archived:", len(archived.All()))

	// Move to a custom folder.
	bob.MoveToFolder(ctx, msgID, "work")
	work, _ := bob.Folder(ctx, "work", store.ListOptions{})
	fmt.Println("work folder:", len(work.All()))
	// Output:
	// inbox: 0 archived: 1
	// work folder: 1
}

// ExampleMailbox_AddTag demonstrates tag management.
func ExampleMailbox_AddTag() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Tagged message",
		Body:         "body",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msgID := inbox.All()[0].GetID()

	// Add tags.
	bob.AddTag(ctx, msgID, "important")
	bob.AddTag(ctx, msgID, "project-x")

	msg, _ := bob.Get(ctx, msgID)
	fmt.Println("tags:", msg.GetTags())

	// Remove a tag.
	bob.RemoveTag(ctx, msgID, "project-x")
	msg, _ = bob.Get(ctx, msgID)
	fmt.Println("after remove:", msg.GetTags())
	// Output:
	// tags: [important project-x]
	// after remove: [important]
}

// ExampleMailbox_threads demonstrates thread-based messaging.
func Example_threads() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")

	// Start a thread.
	msg, _ := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Thread starter",
		Body:         "Let's discuss.",
		ThreadID:     "thread-001",
	})

	// Reply in the same thread.
	bob := svc.Client("bob")
	bobInbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	bobMsgID := bobInbox.All()[0].GetID()
	bob.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"alice"},
		Subject:      "Re: Thread starter",
		Body:         "Sounds good!",
		ThreadID:     "thread-001",
		ReplyToID:    bobMsgID,
	})

	// Get all messages in the thread.
	thread, _ := alice.GetThread(ctx, "thread-001", store.ListOptions{})
	fmt.Println("thread messages:", len(thread.All()))

	// Get replies to the original message.
	replies, _ := bob.GetReplies(ctx, msg.GetID(), store.ListOptions{})
	fmt.Println("replies:", len(replies.All()))
	// Output:
	// thread messages: 2
	// replies: 0
}

// ExampleMailbox_Search demonstrates full-text search.
func ExampleMailbox_Search() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Project Alpha update",
		Body:         "The deployment is complete.",
	})
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Lunch plans",
		Body:         "Pizza or sushi?",
	})

	bob := svc.Client("bob")

	// Search for "deployment".
	results, _ := bob.Search(ctx, store.SearchQuery{Query: "deployment"})
	fmt.Println("found:", len(results.All()))
	if len(results.All()) > 0 {
		fmt.Println("subject:", results.All()[0].GetSubject())
	}
	// Output:
	// found: 1
	// subject: Project Alpha update
}

// ExampleMailbox_Stream demonstrates memory-efficient streaming iteration.
func ExampleMailbox_Stream() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	for i := 1; i <= 5; i++ {
		alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      fmt.Sprintf("Msg %d", i),
			Body:         "body",
		})
	}

	bob := svc.Client("bob")

	// Stream all inbox messages in batches of 2.
	iter, _ := bob.Stream(ctx, []store.Filter{
		store.InFolder(store.FolderInbox),
	}, mailbox.StreamOptions{BatchSize: 2})

	var count int
	for {
		ok, err := iter.Next(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if !ok {
			break
		}
		count++
	}
	fmt.Println("streamed:", count)
	// Output:
	// streamed: 5
}

// ExampleMailbox_BulkDelete demonstrates bulk operations.
func ExampleMailbox_BulkDelete() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	var ids []string
	for i := 1; i <= 3; i++ {
		alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      fmt.Sprintf("Bulk %d", i),
			Body:         "body",
		})
	}

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	for _, m := range inbox.All() {
		ids = append(ids, m.GetID())
	}

	// Bulk delete all messages.
	result, _ := bob.BulkDelete(ctx, ids)
	fmt.Println("deleted:", result.SuccessCount())

	trash, _ := bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	fmt.Println("trash:", len(trash.All()))
	// Output:
	// deleted: 3
	// trash: 3
}

// Example_systemMessages demonstrates sending messages from a system user.
func Example_systemMessages() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	// System sends a notification to bob.
	system := svc.Client("system")
	system.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Welcome to the platform",
		Body:         "<h1>Welcome!</h1><p>Your account is ready.</p>",
		Headers:      map[string]string{"Content-Type": "text/html"},
		Metadata: map[string]any{
			"template":  "welcome",
			"deep_link": "/onboarding",
		},
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]
	fmt.Println("from:", msg.GetSenderID())
	fmt.Println("subject:", msg.GetSubject())
	fmt.Println("content-type:", msg.GetHeaders()["Content-Type"])
	fmt.Println("template:", msg.GetMetadata()["template"])
	// Output:
	// from: system
	// subject: Welcome to the platform
	// content-type: text/html
	// template: welcome
}

// Example_eventSubscription demonstrates subscribing to mailbox events.
func Example_eventSubscription() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	// Track events.
	var mu sync.Mutex
	var received []string

	svc.Events().MessageSent.Subscribe(ctx,
		func(ctx context.Context, _ event.Event[mailbox.MessageSentEvent], data mailbox.MessageSentEvent) error {
			mu.Lock()
			received = append(received, "sent:"+data.Subject)
			mu.Unlock()
			return nil
		},
	)
	svc.Events().MessageReceived.Subscribe(ctx,
		func(ctx context.Context, _ event.Event[mailbox.MessageReceivedEvent], data mailbox.MessageReceivedEvent) error {
			mu.Lock()
			received = append(received, "received:"+data.RecipientID)
			mu.Unlock()
			return nil
		},
	)

	// Send a message (events fire synchronously with channel transport).
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "body",
	})

	// Wait for async event delivery.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	sort.Strings(received)
	for _, e := range received {
		fmt.Println(e)
	}
	mu.Unlock()
	// Output:
	// received:bob
	// sent:Hello
}

// Example_notifications demonstrates real-time notification delivery.
// This example uses the in-memory notify store with channel-based delivery.
// In production, use Redis Streams (notifyredis.New) for cross-instance delivery.
func Example_notifications() {
	ctx := context.Background()
	svc := newTestServiceWithNotifications()
	defer svc.Close(ctx)

	// Open notification stream for bob BEFORE sending the message.
	stream, err := svc.Notifications(ctx, "bob", "")
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	// Send a message to bob in the background.
	go func() {
		time.Sleep(150 * time.Millisecond)
		alice := svc.Client("alice")
		alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Real-time!",
			Body:         "body",
		})
	}()

	// Read notifications until we get the received event.
	// Multiple events may fire (sent, received) — find the one for bob.
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		evt, err := stream.Next(readCtx)
		if err != nil {
			log.Fatal(err)
		}
		if evt.Type == "mailbox.message.received" {
			fmt.Println("type:", evt.Type)
			break
		}
	}
	// Output:
	// type: mailbox.message.received
}

// Example_sseServer demonstrates a complete HTTP server with SSE notifications
// and a client that consumes the stream. This is a self-contained server+client
// example that runs in-process using httptest.
func Example_sseServer() {
	ctx := context.Background()
	svc := newTestServiceWithNotifications()
	defer svc.Close(ctx)

	// --- Server side: SSE endpoint ---
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		userID := r.URL.Query().Get("user_id")
		lastEventID := r.Header.Get("Last-Event-ID")

		stream, err := svc.Notifications(r.Context(), userID, lastEventID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer stream.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for {
			evt, err := stream.Next(r.Context())
			if err != nil {
				return
			}
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", evt.ID, evt.Type, string(evt.Payload))
			flusher.Flush()
		}
	})

	ts := httptest.NewServer(handler)
	defer ts.Close()

	// --- Client side: consume SSE ---
	done := make(chan bool, 1)

	go func() {
		resp, err := http.Get(ts.URL + "?user_id=bob")
		if err != nil {
			done <- false
			return
		}
		defer resp.Body.Close()

		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		done <- n > 0
	}()

	// Give the SSE client time to connect.
	time.Sleep(200 * time.Millisecond)

	// Send a message to trigger an SSE event.
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "SSE test",
		Body:         "body",
	})

	select {
	case ok := <-done:
		fmt.Println("received:", ok)
	case <-time.After(5 * time.Second):
		fmt.Println("received: false")
	}
	// Output:
	// received: true
}

// Example_partialDelivery demonstrates handling partial delivery errors.
func Example_partialDelivery() {
	ctx := context.Background()

	// Set up with a small quota so one recipient hits the limit.
	svc, _ := mailbox.NewService(
		mailbox.WithStore(memory.New()),
		mailbox.WithGlobalQuota(mailbox.QuotaPolicy{
			MaxMessages:  2,
			ExceedAction: mailbox.QuotaActionReject,
		}),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Fill bob's quota.
	for i := 0; i < 2; i++ {
		svc.Client(fmt.Sprintf("sender%d", i)).SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "fill",
			Body:         "body",
		})
	}

	// Try to send to both bob (over quota) and charlie (fine).
	alice := svc.Client("alice")
	msg, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob", "charlie"},
		Subject:      "Partial",
		Body:         "body",
	})

	if pde, ok := mailbox.IsPartialDelivery(err); ok {
		fmt.Println("delivered to:", pde.DeliveredTo)
		fmt.Println("failed:", len(pde.FailedRecipients))
		fmt.Println("message sent:", msg != nil)
	}
	// Output:
	// delivered to: [charlie]
	// failed: 1
	// message sent: true
}

// Example_stats demonstrates aggregate statistics.
func Example_stats() {
	ctx := context.Background()
	// Use a plain service (no event transport) to avoid stats cache interference
	// from other examples running in the same process.
	svc, _ := mailbox.NewService(mailbox.WithStore(memory.New()))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	for i := 0; i < 3; i++ {
		alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      fmt.Sprintf("Msg %d", i),
			Body:         "body",
		})
	}

	bob := svc.Client("bob")

	// Stats returns total messages and unread count.
	stats, _ := bob.Stats(ctx)
	fmt.Println("total:", stats.TotalMessages)
	fmt.Println("unread:", stats.UnreadCount)

	// UnreadCount is a convenience method.
	unread, _ := bob.UnreadCount(ctx)
	fmt.Println("unread count:", unread)
	// Output:
	// total: 3
	// unread: 3
	// unread count: 3
}

// Example_filters demonstrates type-safe message filters.
func Example_filters() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	for i := 1; i <= 5; i++ {
		alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      fmt.Sprintf("Msg %d", i),
			Body:         "body",
		})
	}

	bob := svc.Client("bob")

	// Mark first 2 messages as read.
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	for i, m := range inbox.All() {
		if i < 2 {
			bob.UpdateFlags(ctx, m.GetID(), mailbox.MarkRead())
		}
	}

	// Stream only unread messages using filters.
	iter, _ := bob.Stream(ctx, []store.Filter{
		store.InFolder(store.FolderInbox),
		store.IsReadFilter(false),
	}, mailbox.StreamOptions{})

	var count int
	for {
		ok, _ := iter.Next(ctx)
		if !ok {
			break
		}
		count++
	}
	fmt.Println("unread messages:", count)
	// Output:
	// unread messages: 3
}

// Example_cleanupTrash demonstrates scheduled trash cleanup.
func Example_cleanupTrash() {
	ctx := context.Background()
	memStore := memory.New()
	svc, _ := mailbox.NewService(
		mailbox.WithStore(memStore),
		mailbox.WithTrashRetention(24*time.Hour),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Send and delete a message.
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Old message",
		Body:         "body",
	})
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	bob.Delete(ctx, inbox.All()[0].GetID())

	// Age messages past retention period.
	memStore.AgeMessages(48 * time.Hour)

	// Run cleanup.
	result, _ := svc.CleanupTrash(ctx)
	fmt.Println("cleaned:", result.DeletedCount)
	// Output:
	// cleaned: 1
}

// Example_cleanupExpiredMessages demonstrates message retention cleanup.
func Example_cleanupExpiredMessages() {
	ctx := context.Background()
	memStore := memory.New()
	svc, _ := mailbox.NewService(
		mailbox.WithStore(memStore),
		mailbox.WithMessageRetention(24*time.Hour),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Old message",
		Body:         "body",
	})

	// Age messages past retention period.
	memStore.AgeMessages(48 * time.Hour)

	// Send a fresh message that should survive.
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Fresh message",
		Body:         "body",
	})

	result, _ := svc.CleanupExpiredMessages(ctx)
	fmt.Println("expired:", result.DeletedCount)

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	fmt.Println("surviving:", len(inbox.All()))
	// Output:
	// expired: 2
	// surviving: 1
}

// Example_listFolders demonstrates discovering folders.
func Example_listFolders() {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "test",
		Body:         "body",
	})

	bob := svc.Client("bob")

	// Move to a custom folder.
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	bob.MoveToFolder(ctx, inbox.All()[0].GetID(), "work-queue")

	// List all folders with counts.
	folders, _ := bob.ListFolders(ctx)
	for _, f := range folders {
		if f.MessageCount > 0 {
			fmt.Printf("%s: %d messages (%d unread)\n", f.ID, f.MessageCount, f.UnreadCount)
		}
	}
	// Output:
	// work-queue: 1 messages (1 unread)
}

// Example_encryptedMessage demonstrates E2E encryption: send encrypted, receive and decrypt.
func Example_encryptedMessage() {
	ctx := context.Background()

	// Generate X25519 keypairs for sender and recipient.
	keys := crypto.NewStaticKeyResolver()
	for _, user := range []string{"alice", "bob"} {
		priv := make([]byte, 32)
		rand.Read(priv)
		pub, _ := curve25519.X25519(priv, curve25519.Basepoint)
		keys.AddUser(user, pub, priv)
	}

	// Create service with compression + encryption plugins.
	svc, _ := mailbox.NewService(
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(
			compress.NewPlugin(compress.Gzip),  // compress first
			crypto.NewEncryptionPlugin(keys),    // then encrypt
		),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Alice sends an encrypted message — subject stays searchable.
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Confidential report",
		Body:         "Q3 revenue: $1.2M",
		Metadata:     map[string]any{"department": "finance"},
	})

	// Bob receives and decrypts.
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]

	// Subject is plaintext (searchable).
	fmt.Println("subject:", msg.GetSubject())
	fmt.Println("encrypted:", crypto.IsEncrypted(msg))
	fmt.Println("compressed:", compress.IsCompressed(msg))

	// Body is encrypted — decrypt with Open.
	plaintext, _ := crypto.Open(ctx, msg, "bob", keys)
	fmt.Println("body:", string(plaintext))

	// Application metadata is preserved.
	fmt.Println("department:", msg.GetMetadata()["department"])
	// Output:
	// subject: Confidential report
	// encrypted: true
	// compressed: true
	// body: Q3 revenue: $1.2M
	// department: finance
}

// Example_filterBulkOps demonstrates filter-based bulk operations.
func Example_filterBulkOps() {
	ctx := context.Background()
	svc, _ := mailbox.NewService(mailbox.WithStore(memory.New()))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Send 5 messages to bob.
	for i := 0; i < 5; i++ {
		svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      fmt.Sprintf("Msg %d", i),
			Body:         "body",
		})
	}

	bob := svc.Client("bob")

	// Mark all unread in inbox as read using filter.
	count, _ := bob.UpdateByFilter(ctx, []store.Filter{
		store.InFolder(store.FolderInbox),
		store.IsReadFilter(false),
	}, mailbox.MarkRead())
	fmt.Println("marked read:", count)

	// Move all from alice to archive.
	moved, _ := bob.MoveByFilter(ctx, []store.Filter{
		store.SenderIs("alice"),
	}, store.FolderArchived)
	fmt.Println("archived:", moved)
	// Output:
	// marked read: 5
	// archived: 5
}

// Example_messageTTL demonstrates per-message TTL (time-to-live).
// Messages with TTL are automatically deleted by CleanupExpiredMessages.
func Example_messageTTL() {
	ctx := context.Background()
	memStore := memory.New()
	svc, _ := mailbox.NewService(
		mailbox.WithStore(memStore),
		mailbox.WithMessageRetention(24*time.Hour), // also enables TTL cleanup
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Send a message with 2-hour TTL.
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Ephemeral message",
		Body:         "This self-destructs in 2 hours.",
		TTL:          2 * time.Hour,
	})

	// Message is visible now.
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	fmt.Println("before expiry:", len(inbox.All()))

	// Age messages past TTL.
	memStore.AgeTTLAll(3 * time.Hour)

	// CleanupExpiredMessages deletes TTL-expired messages.
	result, _ := svc.CleanupExpiredMessages(ctx)
	fmt.Println("cleaned:", result.DeletedCount)

	inbox, _ = bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	fmt.Println("after expiry:", len(inbox.All()))
	// Output:
	// before expiry: 1
	// cleaned: 2
	// after expiry: 0
}

// Example_scheduledDelivery demonstrates scheduled message delivery.
// Messages with ScheduleAt are hidden until the scheduled time.
func Example_scheduledDelivery() {
	ctx := context.Background()
	memStore := memory.New()
	svc, _ := mailbox.NewService(mailbox.WithStore(memStore))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Schedule a message for 1 hour from now.
	future := time.Now().UTC().Add(1 * time.Hour)
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Good morning!",
		Body:         "Scheduled for 9am.",
		ScheduleAt:   &future,
	})

	// Bob's inbox is empty — message not yet available.
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	fmt.Println("before schedule:", len(inbox.All()))

	// Age schedule past availability.
	memStore.AgeScheduleAll(2 * time.Hour)

	// Now visible.
	inbox, _ = bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	fmt.Println("after schedule:", len(inbox.All()))
	if len(inbox.All()) > 0 {
		fmt.Println("subject:", inbox.All()[0].GetSubject())
	}
	// Output:
	// before schedule: 0
	// after schedule: 1
	// subject: Good morning!
}
