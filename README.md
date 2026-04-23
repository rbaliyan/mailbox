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
- **First-Class Headers** - Protocol-level `map[string]string` headers (Content-Type, Priority, Correlation-ID) separate from application metadata
- **Draft Composition** - Fluent API for composing messages
- **Thread Support** - Conversation threading with replies
- **Fan-Out with Independent State** - Each recipient gets their own copy to manage
- **Full-Text Search** - Native FTS via Postgres `tsvector` / MongoDB text index (opt-in)
- **Stats with Event-Driven Cache** - Per-user aggregate counts with incremental updates; optional Redis L2 cache
- **File Attachments** - S3, GCS, Azure Blob, and local filesystem backends ([details](docs/attachments.md))
- **Real-Time Events** - Publish message lifecycle events to Redis Streams, NATS, Kafka, or any transport ([details](docs/events.md))
- **Multiple Backends** - MongoDB, PostgreSQL, in-memory storage
- **CEL Rules Engine** - Declarative per-user rules with custom functions and webhook/forward actions
- **Webhook Notifications** - Signed HTTP delivery with exponential backoff via `notify/webhook`
- **Plugin System** - Extensible hooks for send-time validation and filtering ([details](docs/advanced.md#plugin-system))
- **OpenTelemetry** - Built-in tracing and metrics ([details](docs/advanced.md#opentelemetry-integration))
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

    // Create service
    svc, err := mailbox.New(mailbox.Config{},
        mailbox.WithStore(store),
    )
    if err != nil {
        log.Fatal(err)
    }

    // Connect to initialize indexes and start background tasks
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
    Headers:      map[string]string{store.HeaderContentType: "application/json", store.HeaderPriority: "high"},
    Metadata:     map[string]any{"job_id": "j-9281"},
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
    "go.mongodb.org/mongo-driver/v2/mongo"
    "go.mongodb.org/mongo-driver/v2/mongo/options"
    mongostore "github.com/rbaliyan/mailbox/store/mongo"
)

// Application manages the MongoDB client
client, _ := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
defer client.Disconnect(ctx)

// Create store with the client
store := mongostore.New(client,
    mongostore.WithDatabase("myapp"),
    mongostore.WithCollection("messages"),
)

// Optional: enable native full-text search (creates a text index on connect)
store = mongostore.New(client,
    mongostore.WithDatabase("myapp"),
    mongostore.WithFTSEnabled(true),
)
```

### PostgreSQL

```go
import (
    "github.com/jmoiron/sqlx"
    _ "github.com/lib/pq"
    pgstore "github.com/rbaliyan/mailbox/store/postgres"
)

// Application manages the database connection (store/postgres uses *sqlx.DB)
db, _ := sqlx.Connect("postgres", "postgres://localhost/myapp?sslmode=disable")
defer db.Close()

// Create store with the connection
store := pgstore.New(db,
    pgstore.WithTable("messages"),
)

// Alternatively, wrap a plain *sql.DB:
// store := pgstore.NewFromDB(sqlDB, pgstore.WithTable("messages"))

// Optional: enable native full-text search (adds tsvector column + trigger on connect)
store = pgstore.New(db,
    pgstore.WithTable("messages"),
    pgstore.WithFTSEnabled(true),
)
```

> **Note:** `WithFTSEnabled(true)` runs DDL on `Connect()` — it adds a `search_vector tsvector` column, a GIN index, and a trigger to keep the vector current, then backfills existing rows. This is safe to run repeatedly (all DDL is idempotent), but expect a brief delay on first `Connect()` for large tables.

### In-Memory (for testing)

```go
import "github.com/rbaliyan/mailbox/store/memory"

store := memory.New()
```

## Message Headers

Messages have two separate key-value stores:

- **Headers** (`map[string]string`) — protocol-level metadata like Content-Type, Priority, Correlation-ID. Analogous to HTTP headers.
- **Metadata** (`map[string]any`) — application-level arbitrary data. Unchanged from previous versions.

```go
// Via draft composition (fluent API)
draft.SetSubject("Sensor Reading").
    SetBody(jsonBytes).
    SetRecipients("analytics-svc").
    SetHeader(store.HeaderContentType, "application/json").
    SetHeader(store.HeaderSchema, "sensor.reading/v1").
    SetHeader(store.HeaderPriority, "high")
```

Well-known header constants are defined in the `store` package: `HeaderContentType`, `HeaderContentLength` (auto-populated on send), `HeaderSchema`, `HeaderPriority`, `HeaderCorrelationID`, `HeaderExpires`, `HeaderReplyToAddress`, `HeaderCustomID`.

The `content` sub-package provides codec support for structured/binary message bodies using headers:

```go
// Encode: struct -> bytes -> text-safe body + headers
data, _ := json.Marshal(reading)
body, headers, _ := content.EncodeWithHeaders(content.JSON, data, content.WithSchema("sensor.reading/v1"))

// Decode: message -> raw bytes
raw, _ := content.Decode(msg, content.DefaultRegistry())
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
replyDraft.SetSubject("Re: Project Discussion").
    SetBody("Sounds good, I have some ideas.").
    SetRecipients("user123")
replyDraft.ReplyTo(ctx, msg.GetID()) // separate call — returns error
reply, _ := replyDraft.Send(ctx)

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
sendResult, _ := drafts.Send(ctx)
for _, msg := range sendResult.SentMessages() {
    log.Printf("Sent: %s", msg.GetID())
}

// Delete all drafts
result, _ := drafts.Delete(ctx)
```

## Graceful Shutdown

```go
svc, _ := mailbox.New(mailbox.Config{
    TrashCleanupInterval: time.Hour, // automatic background cleanup
},
    mailbox.WithStore(store),
)

// ... use the service ...

// Close waits for in-flight operations and background goroutines to finish
if err := svc.Close(ctx); err != nil {
    log.Printf("Shutdown error: %v", err)
}
```

Shutdown behavior:
- Waits for in-flight send operations to complete
- Closes event subscriptions
- Closes store connections

### Trash Cleanup

`Config.TrashCleanupInterval` starts automatic background cleanup. Alternatively, call it manually from your own scheduler:

```go
svc, _ := mailbox.New(mailbox.Config{
    TrashCleanupInterval: time.Hour, // automatic — or leave zero and call manually
},
    mailbox.WithStore(store),
)

// Manual call (e.g., on demand or in tests):
result, err := svc.CleanupTrash(ctx)
if err != nil {
    log.Printf("Cleanup error: %v", err)
} else {
    log.Printf("Deleted %d expired messages", result.DeletedCount)
}
```

## Distributed Stats Cache (Redis)

For multi-instance deployments, wire a Redis-backed L2 stats cache to avoid repeated database reads:

```go
import (
    "github.com/redis/go-redis/v9"
    statscache "github.com/rbaliyan/mailbox/statscache/redis"
)

rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
cache := statscache.New(rdb,
    statscache.WithTTL(5 * time.Minute),
    statscache.WithPrefix("myapp:stats"),
)

svc, _ := mailbox.New(mailbox.Config{},
    mailbox.WithStore(store),
    mailbox.WithStatsCache(cache),
)
```

Stats are served from the in-process `sync.Map` (L1) first, then from Redis (L2), and only fall through to the database on a cold miss. Event-driven increments propagate to Redis atomically via `HINCRBY`.

## CEL Rules Engine

The `rules` package provides a declarative rules engine backed by [Common Expression Language](https://github.com/google/cel-go):

```go
import "github.com/rbaliyan/mailbox/rules"

provider := rules.NewStaticProvider(
    []rules.Rule{
        {
            ID:        "vip-tag",
            Scope:     rules.ScopeReceive,
            Condition: `contains_tag(tags, "vip") || sender == "admin"`,
            Actions:   []rules.Action{{Type: rules.ActionSetFolder, Value: "priority"}},
            Priority:  10,
        },
        {
            ID:        "webhook-on-send",
            Scope:     rules.ScopeSend,
            Condition: `has_header(headers, "X-Alert")`,
            Actions:   []rules.Action{{Type: rules.ActionWebhook, Value: "https://hooks.example.com/alert"}},
        },
    },
    nil, // per-user rules
)

engine, _ := rules.NewEngine(provider, store)
svc, _ := mailbox.New(mailbox.Config{},
    mailbox.WithStore(store),
    mailbox.WithPlugin(engine),
)
svc.Connect(ctx)

// Wire receive-side rules via the event bus
svc.Events().MessageReceived.Subscribe(ctx,
    func(ctx context.Context, _ event.Event[mailbox.MessageReceivedEvent], data mailbox.MessageReceivedEvent) error {
        return engine.OnMessageReceived(ctx, nil, rules.MessageReceivedData{
            MessageID:   data.MessageID,
            RecipientID: data.RecipientID,
        })
    },
    event.AsWorker("rules-engine"),
)
```

Available CEL variables: `sender`, `subject`, `body`, `recipients`, `headers`, `metadata`, `has_attachments`, `attachment_count`, `thread_id`, `is_reply`, `folder`, `tags`.

Custom functions: `matches_regex`, `has_header`, `header_value`, `has_metadata`, `metadata_value`, `contains_tag`.

## Webhook Notification Router

The `notify/webhook` package delivers notification events to HTTP endpoints with HMAC-SHA256 signing:

```go
import "github.com/rbaliyan/mailbox/notify/webhook"

router := webhook.New(
    webhook.WithEndpoints(
        webhook.EndpointConfig{
            URL:    "https://api.example.com/webhooks/mailbox",
            Events: []string{"message.received", "message.read"},
        },
    ),
    webhook.WithSigningKey([]byte("my-secret")),
)

// On the receiving end, verify the signature:
ok := webhook.VerifySignature(
    []byte("my-secret"),
    r.Header.Get("X-Mailbox-Timestamp"),
    body,
    r.Header.Get("X-Mailbox-Signature"),
)
```

## API Reference

### Mailbox Operations

| Method | Description |
|--------|-------------|
| `Compose()` | Create a new draft message (returns Draft, error) |
| `SendMessage(ctx, req)` | Send a message directly without drafts |
| `Get(ctx, id)` | Get a message by ID |
| `Folder(ctx, folderID, opts)` | List messages in any folder |
| `Drafts(ctx, opts)` | List draft messages |
| `Search(ctx, query)` | Full-text search |
| `Stream(ctx, filters, opts)` | Iterator-based streaming with filters |
| `GetThread(ctx, threadID, opts)` | Get thread messages |
| `GetReplies(ctx, messageID, opts)` | Get message replies |
| `ListFolders(ctx)` | List all folders with counts |
| `Stats(ctx)` | Aggregate statistics (total, unread, per-folder) |
| `UnreadCount(ctx)` | Unread message count |
| `LoadAttachment(ctx, msgID, attID)` | Download attachment |

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

// Fluent setters (chainable)
draft.SetSubject("Meeting Tomorrow").
    SetBody("Let's discuss the project.").
    SetRecipients("user456", "user789").
    SetHeader(store.HeaderPriority, "high").
    SetMetadata("category", "meetings").
    SetTTL(7 * 24 * time.Hour)                  // expires in 7 days
    // SetScheduleAt(time.Now().Add(time.Hour))  // optional: deliver in 1 hour

// Failable operations (separate calls — return error)
draft.AddAttachment(attachment)
draft.ReplyTo(ctx, parentMessageID)

// Send immediately
msg, err := draft.Send(ctx)

// Or save as draft
saved, err := draft.Save(ctx)
```

## Further Reading

- [Real-Time Events](docs/events.md) — Setting up event publishing and subscribing to message lifecycle events
- [File Attachments](docs/attachments.md) — S3, GCS, Azure Blob, and local filesystem attachment storage
- [Advanced Configuration](docs/advanced.md) — Plugins, OpenTelemetry, message limits
- [Architecture](docs/architecture.md) — Design principles, store interface, concurrency model

## License

MIT License
