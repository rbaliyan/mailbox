# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Mailbox is a Go library for email-like messaging. It provides:
- Draft composition with fluent API
- Send messages to recipients (by userID strings, system maps to email/phone via RecipientResolver)
- Mark messages as read/unread
- Move messages between folders
- Tag management
- Soft delete with trash and restore
- Full-text search
- File attachments (S3, GCS)
- Real-time events

All functionality is exposed via interfaces with pluggable backends:
- Storage: MongoDB (`*mongo.Client`), PostgreSQL (`*sql.DB`), In-memory
- Attachments: S3, GCS (with caching and OpenTelemetry wrappers)
- Events: Redis Streams (`redis.UniversalClient`) - optional

**Important:** Store implementations accept database clients directly (not URIs). The application manages client lifecycle.

## Build Commands

```bash
go build ./...                          # Build
go test ./...                           # Run all tests
go test -run TestName ./path/to/pkg     # Run single test
go test -v ./...                        # Verbose tests
go fmt ./...                            # Format code
go vet ./...                            # Vet code
golangci-lint run                       # Lint
```

## Package Structure

```
mailbox/
├── doc.go              # Package documentation
├── errors.go           # Sentinel errors
├── message.go          # Message interface (read-only)
├── draft.go            # DraftMessage interface (mutable)
├── attachment.go       # Attachment type
├── mailbox.go          # Main Mailbox implementation
├── option.go           # Option pattern configuration
├── recipient.go        # Recipient type and RecipientResolver interface
├── events.go           # Event types and bus setup (Redis Streams)
├── validation.go       # Input validation
├── plugin.go           # Plugin/extension system
├── otel.go             # OpenTelemetry integration
├── store/
│   ├── store.go        # Store interface (DraftStore + MessageStore + MaintenanceStore)
│   ├── errors.go       # Store-specific errors
│   ├── filter.go       # Type-safe filter builders
│   ├── message.go      # MessageData for creation
│   ├── attachment.go   # AttachmentStore interface
│   ├── memory/
│   │   ├── store.go    # In-memory implementation
│   │   └── message.go  # Message type
│   ├── mongo/
│   │   ├── store.go    # MongoDB implementation (accepts *mongo.Client)
│   │   └── option.go   # MongoDB options
│   ├── postgres/
│   │   ├── store.go    # PostgreSQL implementation (accepts *sql.DB)
│   │   └── option.go   # PostgreSQL options
│   └── attachment/
│       ├── s3/         # S3 attachment store
│       ├── gcs/        # GCS attachment store
│       ├── cached/     # Caching wrapper
│       └── otel/       # OpenTelemetry wrapper
└── resolver/
    └── static.go       # Static recipient resolver
```

## Architecture

### Key Interfaces

**Service** (server-side management):
- `Connect(ctx)` / `Close(ctx)` - lifecycle management
- `Client(userID)` - returns a Mailbox for a specific user
- `CleanupTrash(ctx)` - manual trash cleanup

**Mailbox** (user-facing API):
- `UserID()` - returns the mailbox owner's user ID
- `Compose()` - start a new draft
- `Get(ctx, id)` - retrieve a message
- `Inbox(ctx, opts)` / `Sent(ctx, opts)` / `Trash(ctx, opts)` - list messages
- `Search(ctx, query)` - full-text search
- `GetThread(ctx, threadID, opts)` / `GetReplies(ctx, messageID, opts)` - thread support

**Store** (storage backend - composed of three sub-interfaces):

*DraftStore* - mutable draft operations:
- `NewDraft(ownerID)` - create empty draft
- `GetDraft(ctx, id)` / `SaveDraft(ctx, draft)` / `DeleteDraft(ctx, id)`
- `ListDrafts(ctx, ownerID, opts)`

*MessageStore* - read-only message operations:
- `Get(ctx, id)` / `Find(ctx, filters, opts)` / `Count(ctx, filters)` / `Search(ctx, query)`
- `MarkRead(ctx, id, read)` / `MoveToFolder(ctx, id, folderID)`
- `AddTag(ctx, id, tagID)` / `RemoveTag(ctx, id, tagID)`
- `Delete(ctx, id)` / `HardDelete(ctx, id)` / `Restore(ctx, id)`
- `CreateMessage(ctx, data)` / `CreateMessages(ctx, data)` - atomic batch creation

*MaintenanceStore* - background task operations:
- `DeleteExpiredTrash(ctx, cutoff)` - atomic trash cleanup

**AttachmentStore** (file storage):
- `Upload(ctx, ownerID, filename, reader)` / `Download(ctx, id)` / `Delete(ctx, id)`
- `GetMetadata(ctx, id)` / `GenerateURL(ctx, id, expiry)`

**RecipientResolver** (user ID to contact info):
- `Resolve(ctx, userID)` / `ResolveBatch(ctx, userIDs)`

### Design Patterns

**Client Injection (Library Pattern):**
```go
// Application creates and manages the database client
mongoClient, _ := mongo.Connect(ctx, options.Client().ApplyURI(uri))
defer mongoClient.Disconnect(ctx)

// Store accepts the client, doesn't manage its lifecycle
store := mongostore.New(mongoClient,
    mongostore.WithDatabase("myapp"),
    mongostore.WithCollection("messages"),
)
```

**Option Pattern:**
```go
svc, err := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithTrashRetention(7 * 24 * time.Hour),
)
```

**New() vs Connect():**
- `New(client, opts...)` creates instance with injected client
- `Connect(ctx)` initializes indexes/schema (can fail)
- `Close(ctx)` marks store as disconnected (caller closes client)

**Soft Delete:**
- Messages use `Deleted` field (stored as `__deleted` in MongoDB)
- All queries automatically filter deleted messages

**Type-Safe Filters:**
```go
filter := store.MessageFilter("SenderID").Is("user123")
filter2 := store.OwnerIs("user456")
filter3 := store.InFolder(store.FolderInbox)
```

### Naming Conventions

- Packages: singular, lowercase (`store`, `resolver`)
- Interfaces: simple names (`Store`, `RecipientResolver`)
- Options: `With` prefix (`WithStore`, `WithDatabase`, `WithTimeout`)
- Constructors: `New` prefix (`New`, `NewStatic`)

### Events

When events are registered via `RegisterEvents`, mailbox publishes events:

```go
// Register events with an event bus
mailbox.RegisterEvents(ctx, bus)

// Subscribe to events
mailbox.EventMessageSent.Subscribe(ctx, handler)
mailbox.EventMessageRead.Subscribe(ctx, handler)
mailbox.EventMessageDeleted.Subscribe(ctx, handler)
```

Events are published after successful operations. Event payloads include message ID, user ID, and timestamps.

### Concurrency

**No Distributed Locks Principle:**

This library is designed to avoid distributed locks entirely. Instead, all concurrency concerns are handled through:

1. **Atomic Database Operations**: Use database-native atomic operations like MongoDB's `findOneAndUpdate` with upsert, or PostgreSQL's `INSERT ON CONFLICT`.

2. **Optimistic Concurrency**: For updates, use version fields or timestamps and let the database reject stale updates. Retry on conflict.

3. **Transactional Batches**: Multi-document operations use database transactions (MongoDB sessions, PostgreSQL transactions) for atomicity, not distributed locks.

**Implementation Details:**
- `sync/atomic` for connection state tracking
- Context for cancellation and timeouts
- MongoDB: `findOneAndUpdate` with upsert, `deleteMany` for atomic operations
- PostgreSQL: `INSERT ON CONFLICT`, `DELETE` with conditions

## Configuration Reference

All configuration is done via the functional options pattern.

### Core Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithStore(store.Store)` | **required** | Storage backend (MongoDB, PostgreSQL, or memory) |
| `WithResolver(RecipientResolver)` | nil | Maps user IDs to contact info |
| `WithLogger(*slog.Logger)` | slog.Default() | Structured logger |
| `WithDefaultTimeout(time.Duration)` | 30s | Default operation timeout |

### Message Limits

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxSubjectLength(int)` | 998 | Max subject characters (RFC 5322) |
| `WithMaxBodySize(int)` | 10MB | Max body size in bytes |
| `WithMaxAttachmentSize(int64)` | 25MB | Max attachment size in bytes |
| `WithMaxAttachmentCount(int)` | 20 | Max attachments per message |
| `WithMaxRecipientCount(int)` | 100 | Max recipients per message |
| `WithMaxMetadataSize(int)` | 64KB | Max total metadata size |
| `WithMaxMetadataKeys(int)` | 100 | Max metadata keys |

### Query Limits

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxQueryLimit(int)` | 100 | Max messages per query (cap) |
| `WithDefaultQueryLimit(int)` | 20 | Default messages per query |

### Concurrency and Performance

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxConcurrentSends(int)` | 10 | Max concurrent send operations |
| `WithShutdownTimeout(time.Duration)` | 30s | Graceful shutdown timeout |

### Trash Management

| Option | Default | Description |
|--------|---------|-------------|
| `WithTrashRetention(time.Duration)` | 30 days | Time before cleanup eligibility |

### Observability

| Option | Default | Description |
|--------|---------|-------------|
| `WithTracing(bool)` | false | Enable OpenTelemetry tracing |
| `WithMetrics(bool)` | false | Enable OpenTelemetry metrics |
| `WithOTel(bool)` | false | Enable both tracing and metrics |
| `WithServiceName(string)` | "mailbox" | Service name for telemetry |
| `WithTracerProvider(trace.TracerProvider)` | global | Custom tracer provider |
| `WithMeterProvider(metric.MeterProvider)` | global | Custom meter provider |

### Extensions

| Option | Default | Description |
|--------|---------|-------------|
| `WithPlugin(Plugin)` | - | Register a single plugin |
| `WithPlugins(...Plugin)` | - | Register multiple plugins |
| `WithAttachmentManager(store.AttachmentManager)` | nil | Reference-counted attachments |

### Example Configuration

```go
svc, err := mailbox.NewService(
    // Required
    mailbox.WithStore(mongoStore),

    // Optional: Performance
    mailbox.WithMaxConcurrentSends(20),
    mailbox.WithShutdownTimeout(60 * time.Second),

    // Optional: Limits
    mailbox.WithMaxBodySize(5 * 1024 * 1024), // 5MB
    mailbox.WithMaxAttachmentSize(50 * 1024 * 1024), // 50MB
    mailbox.WithMaxRecipientCount(500),

    // Optional: Trash
    mailbox.WithTrashRetention(7 * 24 * time.Hour), // 7 days

    // Optional: Observability
    mailbox.WithOTel(true),
    mailbox.WithServiceName("my-mailbox"),
)
```

### Caching

This library does NOT include built-in caching. If you need caching, implement it at the store level using the decorator pattern:

```go
// Wrap your store with a caching decorator
cachedStore := mycache.NewCachedStore(mongoStore, redis.Client, 5*time.Minute)
svc, _ := mailbox.NewService(mailbox.WithStore(cachedStore))
```

### Error Handling

Handle partial delivery:

```go
msg, err := draft.Send(ctx)
if pde, ok := mailbox.IsPartialDelivery(err); ok {
    // Some recipients failed
    fmt.Printf("Delivered to: %v\n", pde.DeliveredTo)
    fmt.Printf("Failed: %v\n", pde.FailedRecipients)
}
```
