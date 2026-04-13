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
├── mailbox.go          # Main Mailbox implementation (New, NewService, Connect, Close)
├── config.go           # Config struct, DefaultConfig, QuotaUserLister
├── background.go       # Background maintenance goroutines
├── option.go           # Option pattern configuration
├── user.go             # User interface, UserResolver, metadata keys
├── recipient.go        # Recipient type and RecipientResolver interface
├── router/
│   └── router.go       # Router (userID→mailboxID) and Registrar interfaces
├── events.go           # Event types and bus setup (Redis Streams)
├── validation.go       # Input validation
├── plugin.go           # Plugin/extension system
├── otel.go             # OpenTelemetry integration
├── notifications.go    # Event-to-notification bridge (AsWorker handlers)
├── presence/
│   ├── presence.go     # Tracker interface, Registration, RoutingInfo
│   ├── memory/
│   │   └── tracker.go  # In-memory presence tracker (testing)
│   └── redis/
│       ├── tracker.go  # Redis hash presence tracker (multi-instance)
│       └── option.go   # Redis presence options
├── notify/
│   ├── notify.go       # Event, Stream, Store, Router interfaces
│   ├── notifier.go     # Notifier (push, subscribe, local delivery, routing)
│   ├── stream.go       # Stream with channel delivery + store polling
│   ├── option.go       # Notifier options
│   └── memory/
│       └── store.go    # In-memory notification store (testing)
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
├── compress/
│   └── compress.go     # Gzip compression plugin (SendHook) + Decompress/Open helpers
├── crypto/
│   ├── crypto.go       # E2E encryption constants, errors, KeyType
│   ├── encrypt.go      # EncryptionPlugin (SendHook)
│   ├── decrypt.go      # Decrypt, Open (client-side helpers)
│   ├── envelope.go     # AES-256-GCM, X25519/RSA-OAEP key wrapping
│   ├── key_resolver.go # KeyResolver, PrivateKeyProvider, StaticKeyResolver
│   └── option.go       # WithKeyType option
├── content/
│   └── codec.go        # Content encoding/decoding with schema support
├── mailboxtest/
│   └── mailboxtest.go  # Test helpers (NewService, SendMessage, X25519Keypair)
└── resolver/
    └── static.go       # Static recipient/user resolver
```

## Architecture

### Service Construction

Two constructors are available:

```go
// Preferred: Config controls background maintenance scheduling.
svc, err := mailbox.New(mailbox.Config{
    TrashCleanupInterval:          1 * time.Hour,
    ExpiredMessageCleanupInterval: 1 * time.Hour,
}, mailbox.WithStore(store))

// Deprecated: equivalent to New(DefaultConfig(), opts...).
// All background tasks disabled; caller schedules maintenance manually.
svc, err := mailbox.NewService(mailbox.WithStore(store))
```

When Config intervals are non-zero, `Connect()` starts background goroutines.
`Close()` cancels them and blocks until all goroutines finish via `sync.WaitGroup`.

### Key Interfaces

**Service** (server-side management):
- `Connect(ctx)` / `Close(ctx)` - lifecycle management
- `Client(userID)` - returns a Mailbox for a specific user
- `CleanupTrash(ctx)` - manual trash cleanup
- `CleanupExpiredMessages(ctx)` - deletes globally expired + per-message TTL-expired messages
- `EnforceQuotas(ctx, userIDs)` - enforce delete-oldest quotas
- `Events()` - per-service event instances
- `Notifications(ctx, userID, lastEventID)` - notification stream

**Mailbox** (user-facing API):
- `UserID()` - returns the mailbox owner's user ID
- `Compose()` - start a new draft
- `SendMessage(ctx, req)` - send without draft workflow
- `Get(ctx, id)` - retrieve a message
- `Folder(ctx, folderID, opts)` - list messages in any folder
- `Search(ctx, query)` - full-text search
- `Stream(ctx, filters, opts)` - iterator-based streaming
- `GetThread(ctx, threadID, opts)` / `GetReplies(ctx, messageID, opts)` - thread support
- `Stats(ctx)` / `UnreadCount(ctx)` - aggregate statistics
- `UpdateByFilter(ctx, filters, flags)` - bulk mark read/unread by filter
- `MoveByFilter(ctx, filters, folderID)` - bulk move by filter
- `DeleteByFilter(ctx, filters)` - bulk soft-delete by filter
- `TagByFilter(ctx, filters, tagID)` / `UntagByFilter(ctx, filters, tagID)` - bulk tag/untag

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
- `DeleteExpiredMessages(ctx, cutoff)` - atomic message retention cleanup
- `DeleteTTLExpiredMessages(ctx, now)` - atomic per-message TTL cleanup
- `DeleteMessagesByIDs(ctx, ids)` - atomic delete with winner reporting

**AttachmentStore** (file storage):
- `Upload(ctx, ownerID, filename, reader)` / `Download(ctx, id)` / `Delete(ctx, id)`
- `GetMetadata(ctx, id)` / `GenerateURL(ctx, id, expiry)`

**RecipientResolver** (user ID to contact info):
- `Resolve(ctx, userID)` / `ResolveBatch(ctx, userIDs)`

**UserResolver** (sender identity enrichment, optional):
- `ResolveUser(ctx, userID)` returns `User` (FirstName, LastName, Email)
- When configured via `WithUserResolver`, populates message metadata during send
- Failure aborts the send with `ErrUserResolveFailed`

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
svc, err := mailbox.New(mailbox.Config{
    TrashCleanupInterval: 1 * time.Hour,
},
    mailbox.WithStore(store),
    mailbox.WithTrashRetention(7 * 24 * time.Hour),
)
```

**New() vs Connect():**
- `New(cfg, opts...)` creates service with config and options
- `Connect(ctx)` initializes indexes/schema and starts background goroutines
- `Close(ctx)` stops background goroutines, waits for in-flight ops, closes store

**Soft Delete:**
- Messages are moved to `__trash` folder (not a separate deleted flag)
- All inbox/sent/archive queries automatically exclude trash

**Type-Safe Filters:**
```go
filter, _ := store.MessageFilter("SenderID").Equal("user123")
filter2 := store.OwnerIs("user456")
filter3 := store.InFolder(store.FolderInbox)
```

### Naming Conventions

- Packages: singular, lowercase (`store`, `resolver`)
- Interfaces: simple names (`Store`, `RecipientResolver`)
- Options: `With` prefix (`WithStore`, `WithDatabase`, `WithTimeout`)
- Constructors: `New` prefix (`New`, `NewStatic`)

### Events

Events are automatically registered during `Connect()` when a Redis client or event transport is provided. Use per-service events via `svc.Events()`:

```go
svc.Events().MessageSent.Subscribe(ctx, handler)
svc.Events().MessageReceived.Subscribe(ctx, handler)
svc.Events().MessageRead.Subscribe(ctx, handler)
svc.Events().MessageDeleted.Subscribe(ctx, handler)
svc.Events().MessageMoved.Subscribe(ctx, handler)
svc.Events().MarkAllRead.Subscribe(ctx, handler)
```

Events are published after successful operations. Event payloads include message ID, user ID, and timestamps.

### Notifications (Real-Time Per-User Streams)

The notification system delivers real-time events to connected users via SSE or similar transports:

- **`presence/`**: Independent module tracking user online/offline status with optional routing info. Redis-backed for multi-instance, memory for testing.
- **`notify/`**: Per-user notification streams. Uses event bus with `AsWorker` (worker model — one instance processes each event). Persistence via `notify.Store` enables backfill on reconnect.
- **`notify.Router`**: Optional cross-instance delivery. When presence carries routing info, events are forwarded directly to the instance holding the user's connection.

```go
// Setup
tracker := predis.New(redisClient, predis.WithTTL(30*time.Second))
notifier := notify.NewNotifier(
    notify.WithStore(notifyStore),
    notify.WithPresence(tracker),
    notify.WithRouter(myRouter),       // optional
    notify.WithInstanceID("web-3"),    // optional
)
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithNotifier(notifier),
)

// SSE handler
reg, _ := tracker.Register(ctx, userID, presence.WithRouting(presence.RoutingInfo{
    InstanceID: "web-3",
}))
defer reg.Unregister(ctx)

stream, _ := svc.Notifications(ctx, userID, lastEventID)
defer stream.Close()

for {
    evt, err := stream.Next(r.Context())
    if err != nil {
        return
    }
    fmt.Fprintf(w, "id: %s\ndata: %s\n\n", evt.ID, evt.Payload)
    flusher.Flush()
}
```

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

Configuration is split between `Config` (passed to `New`) and functional options.

### Config (Background Maintenance)

| Field | Default | Description |
|-------|---------|-------------|
| `TrashCleanupInterval` | 0 (disabled) | How often to run automatic trash cleanup |
| `ExpiredMessageCleanupInterval` | 0 (disabled) | How often to run automatic expired message cleanup |
| `QuotaEnforcementInterval` | 0 (disabled) | How often to run automatic quota enforcement |
| `QuotaUserLister` | nil | Provides user IDs for quota enforcement (required when interval > 0) |

### Core Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithStore(store.Store)` | **required** | Storage backend (MongoDB, PostgreSQL, or memory) |
| `WithLogger(*slog.Logger)` | slog.Default() | Structured logger |

### Message Limits

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxSubjectLength(int)` | 998 | Max subject characters (RFC 5322) |
| `WithMaxBodySize(int)` | 10MB | Max body size in bytes |
| `WithMaxAttachmentSize(int64)` | 25MB | Max attachment size in bytes |
| `WithMaxAttachmentCount(int)` | 20 | Max attachments per message |
| `WithMaxRecipients(int)` | 100 | Max recipients per message |
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

### Trash and Retention

| Option | Default | Description |
|--------|---------|-------------|
| `WithTrashRetention(time.Duration)` | 30 days | Time before trash cleanup eligibility |
| `WithMessageRetention(time.Duration)` | 0 (disabled) | Global message TTL based on created_at. Min 1 day |

### Per-Message TTL and Scheduling

| Option | Default | Description |
|--------|---------|-------------|
| `WithDefaultTTL(time.Duration)` | 0 (disabled) | Default TTL applied when no explicit TTL set |
| `WithMinTTL(time.Duration)` | 1 minute | Minimum allowed TTL; shorter values rejected with `ErrInvalidTTL` |
| `WithMaxTTL(time.Duration)` | 0 (unlimited) | Maximum allowed TTL; longer values rejected with `ErrInvalidTTL` |
| `WithMinScheduleDelay(time.Duration)` | 0 (no min) | Minimum schedule delay from now |
| `WithMaxScheduleDelay(time.Duration)` | 0 (unlimited) | Maximum schedule delay; rejected with `ErrInvalidSchedule` |

Messages support two optional time fields:
- **TTL** (`SendRequest.TTL` / `draft.SetTTL(d)`): message auto-deleted after expiry via `CleanupExpiredMessages`
- **Schedule** (`SendRequest.ScheduleAt` / `draft.SetScheduleAt(t)`): message hidden from queries until the scheduled time

When both are set, the TTL starts from the scheduled delivery time, not from send time. A message scheduled for 1h with 30m TTL expires at 1h30m.

### Observability

| Option | Default | Description |
|--------|---------|-------------|
| `WithTracing(bool)` | false | Enable OpenTelemetry tracing |
| `WithMetrics(bool)` | false | Enable OpenTelemetry metrics |
| `WithOTel(bool)` | false | Enable both tracing and metrics |
| `WithServiceName(string)` | "mailbox" | Service name for telemetry |
| `WithTracerProvider(trace.TracerProvider)` | global | Custom tracer provider |
| `WithMeterProvider(metric.MeterProvider)` | global | Custom meter provider |

### Notifications

| Option | Default | Description |
|--------|---------|-------------|
| `WithNotifier(*notify.Notifier)` | nil | Per-user notification system |
| `WithNotificationCoalescing(bool)` | false | Coalesce events by message ID (latest wins) |
| `WithStatsRefreshInterval(time.Duration)` | 30s | TTL for cached stats (event-driven invalidation) |

Notifier options (`notify.NewNotifier(...)`):

| Option | Default | Description |
|--------|---------|-------------|
| `notify.WithStore(Store)` | nil | Notification persistence for backfill |
| `notify.WithPresence(Tracker)` | nil | Skip pushes for offline users |
| `notify.WithRouter(Router)` | nil | Cross-instance event delivery |
| `notify.WithInstanceID(string)` | "" | This instance's ID for routing |
| `notify.WithPollInterval(time.Duration)` | 2s | Store poll interval for cross-instance events |
| `notify.WithBufferSize(int)` | 64 | Channel buffer size for local delivery |

### Quota

| Option | Default | Description |
|--------|---------|-------------|
| `WithQuotaProvider(QuotaProvider)` | nil | Custom per-user quota provider |
| `WithGlobalQuota(QuotaPolicy)` | - | Uniform quota for all users |

### Events

| Option | Default | Description |
|--------|---------|-------------|
| `WithRedisClient(redis.UniversalClient)` | nil | Redis Streams event transport |
| `WithEventTransport(transport.Transport)` | nil | Custom event transport |
| `WithEventErrorsFatal(bool)` | false | Fail operations on event publish error |
| `WithEventPublishFailureHandler(fn)` | logger.Error | Callback for event publish failures |

### Redis Event Transport Tuning

| Option | Default | Description |
|--------|---------|-------------|
| `WithClaimInterval(interval, minIdle)` | 30s, 60s | Orphan message claiming from dead consumers |
| `WithClaimBatchSize(int64)` | 100 | Max messages to claim per cycle |
| `WithEventStreamMaxLen(int64)` | 0 (unlimited) | Max entries per Redis event stream |

### Extensions

| Option | Default | Description |
|--------|---------|-------------|
| `WithPlugin(Plugin)` | - | Register a single plugin |
| `WithPlugins(...Plugin)` | - | Register multiple plugins |
| `WithAttachmentManager(store.AttachmentManager)` | nil | Reference-counted attachments |
| `WithUserResolver(UserResolver)` | nil | Sender identity enrichment (sets sender.firstname, sender.lastname, sender.email metadata) |
| `WithRegistrar(router.Registrar)` | nil | Registers this mailbox instance during Connect; assigned ID available via `Service.MailboxID()` |

### Transactional Outbox

Outbox is configured per-store, not per-service:

```go
// MongoDB
store := mongostore.New(client, mongostore.WithOutbox(true))

// PostgreSQL
store := pgstore.New(db, pgstore.WithOutbox(true))
```

When enabled, mutation methods atomically persist events to an outbox table/collection
in the same database transaction. The event bus auto-routes `Event.Publish()` to the
outbox table via `event.WithOutboxTx` (set by the store's `WithOutboxCtx`). A background
relay (`outbox.Relay`) publishes pending events to the transport — no custom serialization needed.

Store interfaces: `store.OutboxPersister` (`OutboxEnabled`, `WithOutboxCtx`) and
`store.EventOutboxProvider` (exposes `event.OutboxStore` for bus-level integration).

### Multi-Instance Routing (router package)

The `router` package defines two interfaces for multi-mailbox deployments:

- **`router.Router`** — `Route(ctx, userID) (mailboxID, error)`. Consulted by
  an orchestrator to resolve which mailbox instance owns a user's messages.
- **`router.Registrar`** — `Register(ctx) (mailboxID, error)`. Called by the
  mailbox itself during `Connect`. The registrar returns the mailbox ID
  assigned to this instance; a registration failure aborts `Connect`.

```go
svc, _ := mailbox.New(cfg,
    mailbox.WithStore(store),
    mailbox.WithRegistrar(myRegistrar),
)
if err := svc.Connect(ctx); err != nil {
    // Registration failures bubble up here
    log.Fatal(err)
}
id := svc.MailboxID() // assigned by the registrar
```

### Selective Delivery (DeliverTo)

`SendRequest.DeliverTo` and `DraftComposer.SetDeliverTo` separate delivery targets from
message recipients. The message stores the full `RecipientIDs` list (so any reader sees
who else received it), but only `DeliverTo` recipients get inbox copies on this instance.

```go
// Send to bob and charlie, but only deliver locally to bob.
// An external orchestrator handles charlie's delivery on another instance.
msg, _ := alice.SendMessage(ctx, mailbox.SendRequest{
    RecipientIDs: []string{"bob", "charlie"}, // stored in message
    DeliverTo:    []string{"bob"},             // inbox copy created here
    Subject:      "Hello",
    Body:         "World",
})

// Via draft flow
draft.SetRecipients("bob", "charlie").SetDeliverTo("bob")
```

When `DeliverTo` is empty (default), all `RecipientIDs` receive inbox copies — backward compatible.

### Filter-Based Bulk Operations

All filter-based bulk ops auto-scope to the user's non-draft messages. Uses `store.BulkUpdater`
fast path (native `updateMany`/`UPDATE WHERE`) when available, falls back to paginated iteration.

```go
bob.UpdateByFilter(ctx, []store.Filter{
    store.InFolder(store.FolderInbox),
    store.IsReadFilter(false),
}, mailbox.MarkRead())

bob.MoveByFilter(ctx, []store.Filter{
    store.SenderIs("alice"),
}, store.FolderArchived)
```

### Example Configuration

```go
svc, err := mailbox.New(
    // Config: background maintenance scheduling
    mailbox.Config{
        TrashCleanupInterval:          1 * time.Hour,
        ExpiredMessageCleanupInterval: 1 * time.Hour,
    },
    // Required
    mailbox.WithStore(mongoStore),

    // Optional: Performance
    mailbox.WithMaxConcurrentSends(20),
    mailbox.WithShutdownTimeout(60 * time.Second),

    // Optional: Limits
    mailbox.WithMaxBodySize(5 * 1024 * 1024), // 5MB
    mailbox.WithMaxAttachmentSize(50 * 1024 * 1024), // 50MB
    mailbox.WithMaxRecipients(500),

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
