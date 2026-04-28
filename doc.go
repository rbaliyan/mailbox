// Package mailbox provides an email-like messaging library for Go.
//
// It supports sending messages to recipients (identified by user IDs),
// marking messages as read/unread, archiving, listing, and searching.
// All functionality is exposed via interfaces, with pluggable storage
// backends (MongoDB, PostgreSQL, in-memory).
//
// # Basic Usage
//
//	// Create in-memory store for testing
//	store := memory.New()
//
//	// Create mailbox service with automatic background maintenance
//	svc, err := mailbox.New(
//	    mailbox.Config{
//	        TrashCleanupInterval:          1 * time.Hour,
//	        ExpiredMessageCleanupInterval: 1 * time.Hour,
//	    },
//	    mailbox.WithStore(store),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Connect initializes indexes/schema and starts background tasks
//	if err := svc.Connect(ctx); err != nil {
//	    log.Fatal(err)
//	}
//	defer svc.Close(ctx) // stops background goroutines and waits
//
//	// Get a mailbox client for a user
//	mb := svc.Client("user123")
//
//	// Send a message
//	draft, _ := mb.Compose()
//	msg, err := draft.
//	    SetSubject("Hello").
//	    SetBody("World").
//	    SetRecipients("user456").
//	    Send(ctx)
//
// # Mailbox Operations
//
//   - Compose: Create and send messages
//   - Get: Retrieve a message by ID
//   - Inbox/Sent/Archived/Trash: List messages
//   - Search: Full-text search
//   - Stream: Iterator-based streaming with filters
//
// # Storage Backends
//
// The store package provides implementations for:
//   - MongoDB (store/mongo) - accepts *mongo.Client
//   - PostgreSQL (store/postgres) - accepts *sql.DB
//   - In-memory (store/memory) - for testing
//
// # Events
//
// Mailbox provides typed events for message lifecycle notifications.
// Events use the github.com/rbaliyan/event/v3 library which supports
// multiple transports (Redis Streams, NATS, Kafka, in-memory channel).
//
// To enable events, pass WithRedisClient or WithEventTransport when creating the service:
//
//	svc, err := mailbox.New(mailbox.Config{},
//	    mailbox.WithStore(store),
//	    mailbox.WithRedisClient(redisClient),
//	)
//
// Events are automatically registered during Connect(). Access per-service
// events via the Events() method:
//
//	events := svc.Events()
//	events.MessageSent.Subscribe(ctx, handler)
//	events.MessageRead.Subscribe(ctx, handler)
//	events.MessageDeleted.Subscribe(ctx, handler)
//
// Available events:
//   - MessageSent - when a message is sent
//   - MessageReceived - when a message is delivered to a recipient
//   - MessageRead - when a message is marked as read
//   - MessageDeleted - when a message is permanently deleted
//   - MessageMoved - when a message is moved between folders
//   - MarkAllRead - when all messages in a folder are marked as read
//
// # Threads
//
// Every sent message is automatically assigned a thread ID. When no ThreadID
// is provided, one is generated as follows:
//  1. If ReplyToID is set and the sender owns the referenced message,
//     the thread ID is inherited from that message (or the message ID itself
//     is used as the thread root for the first reply to a threadless message).
//  2. Otherwise a new UUID is generated, starting a fresh conversation.
//
// Access per-user thread views via the Mailbox interface:
//
//	thread, _ := mb.GetThread(ctx, threadID, store.ListOptions{Limit: 50})
//	replies, _ := mb.GetReplies(ctx, messageID, store.ListOptions{Limit: 20})
//
// For external delivery systems (e.g., SMTP gateways) that know only the
// thread_id and need to find all participants:
//
//	participants, err := svc.ThreadParticipants(ctx, threadID)
//	if err != nil { ... } // store.ErrNotFound when thread does not exist
//
//	_, err = svc.Client(senderID).SendMessage(ctx, mailbox.SendRequest{
//	    RecipientIDs: participants,
//	    ThreadID:     threadID,
//	    Subject:      "Re: original",
//	    Body:         body,
//	})
//
// ThreadParticipants is a cross-owner query (not scoped to a single user) so
// it lives on Service rather than Mailbox.
//
// # Transactional Outbox
//
// For guaranteed event delivery, enable the transactional outbox on your store:
//
//	store := mongostore.New(client, mongostore.WithOutbox(true))
//
// Events are then persisted atomically with DB writes via the event library's
// outbox package. The store provides an event.OutboxStore to the bus, and a
// background relay (outbox.Relay) publishes pending events to the transport.
// See store.OutboxPersister and store.EventOutboxProvider for the interfaces.
package mailbox
