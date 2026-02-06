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
//	// Create mailbox service
//	svc, err := mailbox.NewService(
//	    mailbox.WithStore(store),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Connect initializes indexes/schema
//	if err := svc.Connect(ctx); err != nil {
//	    log.Fatal(err)
//	}
//	defer svc.Close(ctx)
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
//	svc, err := mailbox.NewService(
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
//   - MessageRead - when a message is marked as read
//   - MessageDeleted - when a message is permanently deleted
package mailbox
