# Mailbox

[![CI](https://github.com/rbaliyan/mailbox/actions/workflows/ci.yml/badge.svg)](https://github.com/rbaliyan/mailbox/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/rbaliyan/mailbox.svg)](https://pkg.go.dev/github.com/rbaliyan/mailbox)
[![Go Report Card](https://goreportcard.com/badge/github.com/rbaliyan/mailbox)](https://goreportcard.com/report/github.com/rbaliyan/mailbox)
[![Release](https://img.shields.io/github/v/release/rbaliyan/mailbox)](https://github.com/rbaliyan/mailbox/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/rbaliyan/mailbox/badge)](https://scorecard.dev/viewer/?uri=github.com/rbaliyan/mailbox)

A Go library for persistent, addressable messaging between users or services. Every message has an owner, a lifecycle, and mutable state — making it suitable for user-to-user communication, service-to-service coordination, and async job delivery where sender and receiver don't need to be online at the same time.

## Why Mailbox

Most messaging infrastructure (Kafka, Redis Streams, NATS) treats messages as immutable records in a stream — consumed, acknowledged, and forgotten. Mailbox treats messages as **documents in an addressable namespace**: they persist until explicitly removed, carry mutable state (read/unread, folders, tags), and support rich queries. This makes it the right fit when:

- Messages are **addressed to a specific entity** (user, service, worker) rather than broadcast to a topic
- Recipients need to **query, filter, and manage** their messages — not just consume a stream
- Sender and receiver may **not exist at the same time** — messages wait in the recipient's mailbox
- Each message has a **lifecycle** beyond produce/consume — drafts, delivery, read receipts, archival, deletion
- **Independent per-recipient state** matters — one recipient deletes their copy while others keep theirs

## Use Cases

**User-to-user messaging** — in-app messaging, support tickets, notifications with read tracking, threaded conversations, folder organization.

**Async service coordination** — Service A sends a task to Service B's mailbox. Service B reads it when it starts, processes it, marks it read. If Service B crashes and restarts, unread messages are still in its inbox. Results go back to Service A's mailbox. Metadata carries structured payloads; tags categorize work; folders separate priorities.

**Job delivery with persistence** — A job producer fans out work to worker mailboxes. Workers query their inbox for unprocessed (unread) jobs, process them, and mark them done. Failed jobs stay in the inbox. No message is lost because delivery is idempotent and storage is durable.

**Audit-friendly communication** — Messages persist with user-controlled retention. Soft delete allows recovery. Every state change (send, read, delete) can publish events for downstream logging.

## Features

- **Persistent Addressable Messaging** - Messages belong to owners, persist until removed
- **Idempotent Delivery** - Per-recipient deduplication with safe retry after partial failure
- **Mutable Message State** - Read/unread, folders, tags, metadata on every message
- **Draft Composition** - Fluent API for composing messages
- **Thread Support** - Conversation threading with replies
- **Fan-Out with Independent State** - Each recipient gets their own copy to manage
- **Full-Text Search** - Search across subject, body, and metadata
- **Stats with Event-Driven Cache** - Per-user aggregate counts with incremental updates
- **File Attachments** - S3 and GCS with reference counting and deduplication
- **Real-Time Events** - Publish message lifecycle events to Redis Streams, NATS, Kafka, or any transport
- **Multiple Backends** - MongoDB, PostgreSQL, in-memory storage
- **Plugin System** - Extensible hooks for send-time validation and filtering
- **OpenTelemetry** - Built-in tracing and metrics
- **Soft Delete** - Trash with restore and configurable retention cleanup

## Installation

```bash
go get github.com/rbaliyan/mailbox
```

## Quick Start

```go
package main

import (
    "context"
    "log"

    "github.com/rbaliyan/mailbox"
    "github.com/rbaliyan/mailbox/store/memory"
)

func main() {
    ctx := context.Background()

    // Create in-memory store
    store := memory.New()

    // Create service with options
    svc, err := mailbox.NewService(
        mailbox.WithStore(store),
    )
    if err != nil {
        log.Fatal(err)
    }

    // Connect to initialize
    if err := svc.Connect(ctx); err != nil {
        log.Fatal(err)
    }
    defer svc.Close(ctx)

    // Get a mailbox client for a user
    mb := svc.Client("user123")

    // Create and send a message
    draft, err := mb.Compose()
    if err != nil {
        log.Fatal(err)
    }
    msg, err := draft.
        SetSubject("Hello").
        SetBody("World").
        SetRecipients("user456").
        Send(ctx)
    if err != nil {
        log.Fatal(err)
    }

    log.Printf("Sent message: %s", msg.GetID())
}
```

### Service-to-Service Example

Services use mailboxes the same way users do. A service's identity is just a string.

```go
// Job producer: send work to the image-resizer service
producer := svc.Client("api-gateway")
_, err := producer.SendMessage(ctx, mailbox.SendRequest{
    RecipientIDs: []string{"image-resizer"},
    Subject:      "resize",
    Body:         `{"url": "s3://bucket/photo.jpg", "width": 800}`,
    Metadata:     map[string]any{"job_id": "j-9281", "priority": "high"},
})

// Job consumer: process pending work (possibly on a different host, started later)
worker := svc.Client("image-resizer")
inbox, _ := worker.Folder(ctx, store.FolderInbox, store.ListOptions{})
for _, job := range inbox.All() {
    process(job.GetBody(), job.GetMetadata())
    worker.UpdateFlags(ctx, job.GetID(), mailbox.Flags{Read: boolPtr(true)})
}
```

Messages wait in the recipient's inbox regardless of whether the consumer is running. Restarted consumers pick up where they left off by querying for unread messages.

## Storage Backends

### MongoDB

```go
import (
    "go.mongodb.org/mongo-driver/mongo"
    "go.mongodb.org/mongo-driver/mongo/options"
    mongostore "github.com/rbaliyan/mailbox/store/mongo"
)

// Application manages the MongoDB client
client, _ := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://localhost:27017"))
defer client.Disconnect(ctx)

// Create store with the client
store := mongostore.New(client,
    mongostore.WithDatabase("myapp"),
    mongostore.WithCollection("messages"),
)
```

### PostgreSQL

```go
import (
    "database/sql"
    _ "github.com/lib/pq"
    pgstore "github.com/rbaliyan/mailbox/store/postgres"
)

// Application manages the database connection
db, _ := sql.Open("postgres", "postgres://localhost/myapp?sslmode=disable")
defer db.Close()

// Create store with the connection
store := pgstore.New(db,
    pgstore.WithTable("messages"),
)
```

### In-Memory (for testing)

```go
import "github.com/rbaliyan/mailbox/store/memory"

store := memory.New()
```

## Caching

This library does **not** include built-in caching. Caching is an infrastructure concern
that varies significantly based on deployment architecture (single instance, multi-instance,
serverless, etc.).

If you need caching, implement it at the store level using the decorator pattern:

```go
// Example: Wrap your store with a caching decorator
cachedStore := mycache.NewCachedStore(mongoStore, redis.Client, 5*time.Minute)
svc, _ := mailbox.NewService(mailbox.WithStore(cachedStore))
```

For cross-deployment synchronization, subscribe to events:

```go
// Subscribe to events for real-time updates
mailbox.EventMessageSent.Subscribe(ctx, func(ctx context.Context, ev event.Event[mailbox.MessageSentEvent], data mailbox.MessageSentEvent) error {
    // Handle new message notification across all deployments
    return nil
})
```

## Thread & Conversation Support

Messages can be organized into threads for conversation tracking:

```go
// Send initial message (creates new thread)
draft, _ := mb.Compose()
msg, _ := draft.
    SetSubject("Project Discussion").
    SetBody("Let's discuss the roadmap.").
    SetRecipients("user456").
    Send(ctx)

// Reply to the message (joins the thread)
replyDraft, _ := mb.Compose()
reply, _ := replyDraft.
    SetSubject("Re: Project Discussion").
    SetBody("Sounds good, I have some ideas.").
    SetRecipients("user123").
    ReplyTo(msg.GetID()).
    Send(ctx)

// Get all messages in a thread
thread, _ := mb.GetThread(ctx, msg.GetThreadID(), store.ListOptions{})
for _, m := range thread.All() {
    fmt.Printf("%s: %s\n", m.GetSenderID(), m.GetSubject())
}

// Get direct replies to a message
replies, _ := mb.GetReplies(ctx, msg.GetID(), store.ListOptions{})
```

## Bulk Operations

Perform operations on multiple messages at once:

```go
// Get inbox messages
inbox, _ := mb.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 50})

// Mark all as read
result, _ := inbox.MarkRead(ctx)
if result.HasFailures() {
    log.Printf("Failed to mark %d messages", len(result.FailedIDs))
}

// Move all to a folder
result, _ := inbox.Move(ctx, "important")

// Delete all
result, _ := inbox.Delete(ctx)

// Archive all (move to archive folder)
result, _ := inbox.Move(ctx, store.FolderArchived)

// Add tag to all
result, _ := inbox.AddTag(ctx, "processed")
```

Draft bulk operations:

```go
// Get all drafts
drafts, _ := mb.Drafts(ctx, store.ListOptions{})

// Send all drafts
sendResult, _ := drafts.SendAll(ctx)
fmt.Printf("Sent %d messages\n", sendResult.SuccessCount)
for id, err := range sendResult.Failed {
    log.Printf("Failed to send %s: %v", id, err)
}

// Delete all drafts
result, _ := drafts.DeleteAll(ctx)
```

## Plugin System

Plugins can hook into message sending for validation, spam filtering, or rate limiting:

```go
// Create a plugin for spam filtering
type SpamFilter struct{}

func (p *SpamFilter) Name() string { return "spam-filter" }
func (p *SpamFilter) Init(ctx context.Context) error { return nil }
func (p *SpamFilter) Close(ctx context.Context) error { return nil }

func (p *SpamFilter) BeforeSend(ctx context.Context, userID string, draft store.DraftMessage) error {
    if containsSpam(draft.GetBody()) {
        return errors.New("message blocked: spam detected")
    }
    return nil
}

func (p *SpamFilter) AfterSend(ctx context.Context, userID string, msg store.Message) {
    log.Printf("Message %s sent by %s", msg.GetID(), userID)
}

// Register the plugin
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithPlugin(&SpamFilter{}),
)
```

For observing other operations (read, delete, archive), use the event system instead.

## File Attachments

### S3

```go
import (
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/s3"
    s3store "github.com/rbaliyan/mailbox/store/attachment/s3"
)

cfg, _ := config.LoadDefaultConfig(ctx)
s3Client := s3.NewFromConfig(cfg)

attachmentStore := s3store.New(s3Client,
    s3store.WithBucket("my-attachments"),
    s3store.WithPrefix("mailbox/"),
)

svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithAttachmentStore(attachmentStore),
)
```

### GCS

```go
import (
    "cloud.google.com/go/storage"
    gcsstore "github.com/rbaliyan/mailbox/store/attachment/gcs"
)

gcsClient, _ := storage.NewClient(ctx)

attachmentStore := gcsstore.New(gcsClient,
    gcsstore.WithBucket("my-attachments"),
    gcsstore.WithPrefix("mailbox/"),
)
```

## Real-Time Events

### Setting Up Events

```go
import (
    "github.com/redis/go-redis/v9"
    "github.com/rbaliyan/event/v3"
    eventredis "github.com/rbaliyan/event/v3/redis"
)

// Create Redis client for event transport
redisClient := redis.NewUniversalClient(&redis.UniversalOptions{
    Addrs: []string{"localhost:6379"},
})

// Create event bus with Redis transport
bus, _ := event.NewBus("myapp", event.WithTransport(eventredis.New(redisClient)))

// Register mailbox events with the bus
mailbox.RegisterEvents(ctx, bus)

// Create service with event client
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithEventClient(redisClient),
)
```

### Subscribing to Events

```go
// Message sent event
mailbox.EventMessageSent.Subscribe(ctx, func(ctx context.Context, ev event.Event[mailbox.MessageSentEvent], data mailbox.MessageSentEvent) error {
    log.Printf("Message sent: %s to %v", data.MessageID, data.RecipientIDs)
    return nil
})

// Message read event
mailbox.EventMessageRead.Subscribe(ctx, func(ctx context.Context, ev event.Event[mailbox.MessageReadEvent], data mailbox.MessageReadEvent) error {
    log.Printf("Message %s read by %s", data.MessageID, data.UserID)
    return nil
})

// Message deleted event (permanent deletions only)
mailbox.EventMessageDeleted.Subscribe(ctx, func(ctx context.Context, ev event.Event[mailbox.MessageDeletedEvent], data mailbox.MessageDeletedEvent) error {
    log.Printf("Message %s permanently deleted by %s", data.MessageID, data.UserID)
    return nil
})
```

### Available Events

Events are optional - they use a no-op transport by default and are silently
skipped if `RegisterEvents()` is not called. This allows the library to work
without any event infrastructure.

| Event | Description |
|-------|-------------|
| `EventMessageSent` | Message was sent (primary event for recipient notifications) |
| `EventMessageRead` | Message was marked as read (for read receipts) |
| `EventMessageDeleted` | Message permanently deleted |

## OpenTelemetry Integration

Built-in support for distributed tracing and metrics:

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/sdk/trace"
)

// Set up OpenTelemetry provider
tp := trace.NewTracerProvider(/* ... */)
otel.SetTracerProvider(tp)

// Enable tracing and metrics
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithOTel(true),                    // Enable both tracing and metrics
    mailbox.WithServiceName("my-mailbox"),     // Custom service name
)

// Or enable individually
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithTracing(true),   // Tracing only
    mailbox.WithMetrics(true),   // Metrics only
)
```

Tracked metrics:
- `mailbox.messages.sent` - Messages sent counter
- `mailbox.messages.received` - Messages received counter
- `mailbox.operations.duration` - Operation latency histogram

## Message Limits

Configure the most commonly adjusted limits:

```go
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithMaxBodySize(5 * 1024 * 1024),       // Default: 10 MB
    mailbox.WithMaxAttachmentSize(10 * 1024 * 1024), // Default: 25 MB
    mailbox.WithMaxRecipients(50),                   // Default: 100
)
```

Default limits (not configurable, sensible for most use cases):
- Subject: 998 characters (RFC 5322)
- Attachment count: 20 per message
- Metadata: 64 KB, 100 keys max
- Query limit: 100 messages (default 20)

## Graceful Shutdown

The service handles graceful shutdown automatically:

```go
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithShutdownTimeout(60 * time.Second), // Wait up to 60s for operations
)

// ... use the service ...

// Close waits for in-flight operations to complete
if err := svc.Close(ctx); err != nil {
    log.Printf("Shutdown error: %v", err)
}
```

Shutdown behavior:
- Waits for in-flight send operations to complete
- Closes event subscriptions
- Closes store connections

### Trash Cleanup

Trash cleanup is not automatically started. Call `CleanupTrash()` from your own scheduler:

```go
// Run cleanup periodically using your application's scheduler
result, err := svc.CleanupTrash(ctx)
if err != nil {
    log.Printf("Cleanup error: %v", err)
} else {
    log.Printf("Deleted %d expired messages", result.DeletedCount)
}
```

## API Reference

### Mailbox Operations

| Method | Description |
|--------|-------------|
| `Compose()` | Create a new draft message (returns Draft, error) |
| `Get(ctx, id)` | Get a message by ID |
| `Inbox(ctx, opts)` | List inbox messages |
| `Sent(ctx, opts)` | List sent messages |
| `Archived(ctx, opts)` | List archived messages |
| `Trash(ctx, opts)` | List trashed messages |
| `Drafts(ctx, opts)` | List draft messages |
| `Folder(ctx, folderID, opts)` | List messages in folder |
| `Search(ctx, query)` | Full-text search |
| `GetThread(ctx, threadID, opts)` | Get thread messages |
| `GetReplies(ctx, messageID, opts)` | Get message replies |
| `ListFolders(ctx)` | List all folders with counts |
| `LoadAttachment(ctx, msgID, attID)` | Download attachment |
| `ResolveRecipients(ctx, userIDs)` | Resolve recipient info |

### Message Operations

| Method | Description |
|--------|-------------|
| `msg.Update(ctx, ...flags)` | Update message flags |
| `msg.Move(ctx, folderID)` | Move to folder |
| `msg.Delete(ctx)` | Move to trash |
| `msg.Restore(ctx)` | Restore from trash |
| `msg.PermanentlyDelete(ctx)` | Permanently delete |
| `msg.AddTag(ctx, tagID)` | Add tag |
| `msg.RemoveTag(ctx, tagID)` | Remove tag |

### Draft Builder

```go
draft, _ := mb.Compose()
draft.
    SetSubject("Meeting Tomorrow").
    SetBody("Let's discuss the project.").
    SetRecipients("user456", "user789").
    SetMetadata("priority", "high").
    AddAttachment(attachment).
    ReplyTo(parentMessageID)

// Send immediately
msg, err := draft.Send(ctx)

// Or save as draft
saved, err := draft.Save(ctx)
```

## Architecture

### Addressable, Not Topic-Based

Unlike message brokers where you publish to topics and consumers subscribe, Mailbox delivers to named recipients. Each recipient gets an independent copy with its own state. This is the same model as email (IMAP/JMAP) — but as a library, without protocol overhead.

```
Producer ──send──> Mailbox Store ──query──> Consumer
                   (persists)               (reads when ready)
```

Producers and consumers are temporally decoupled. Messages persist in the store and can be queried, filtered, and managed by the recipient at any point. This makes it natural for async workflows where the consumer may not be running when the message is sent.

### No Distributed Locks

All concurrency is handled through database-native atomicity:

1. **Atomic Database Operations** - MongoDB `findOneAndUpdate`, PostgreSQL `INSERT ON CONFLICT`
2. **Idempotent Delivery** - Per-recipient deduplication keys prevent duplicates on retry
3. **Transactional Batches** - Database transactions for multi-document atomicity

### Store Interface

The store is composed of four sub-interfaces:

- **DraftStore** - Mutable draft operations
- **MessageStore** - Message queries and state mutations
- **MaintenanceStore** - Background maintenance operations
- **StatsStore** - Aggregate mailbox statistics

### Service vs Client

- **Service** - Singleton that manages connections, configuration, and shared resources
- **Client** - Lightweight, per-identity handle (obtained via `svc.Client(id)`) — works for users and services alike

## License

MIT License
