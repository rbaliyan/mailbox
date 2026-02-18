# Advanced Configuration

## Caching

This library does **not** include built-in caching. Caching is an infrastructure concern that varies significantly based on deployment architecture (single instance, multi-instance, serverless, etc.).

If you need caching, implement it at the store level using the decorator pattern:

```go
// Example: Wrap your store with a caching decorator
cachedStore := mycache.NewCachedStore(mongoStore, redis.Client, 5*time.Minute)
svc, _ := mailbox.NewService(mailbox.WithStore(cachedStore))
```

For cross-deployment synchronization, subscribe to [events](events.md):

```go
// Subscribe to events for real-time updates
mailbox.EventMessageSent.Subscribe(ctx, func(ctx context.Context, ev event.Event[mailbox.MessageSentEvent], data mailbox.MessageSentEvent) error {
    // Handle new message notification across all deployments
    return nil
})
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

For observing other operations (read, delete, archive), use the [event system](events.md) instead.

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
    mailbox.WithMaxHeaderCount(25),                  // Default: 50
)
```

Default limits:
- Subject: 998 characters (RFC 5322)
- Attachment count: 20 per message
- Metadata: 64 KB, 100 keys max
- Headers: 50 max, 128-byte keys, 8 KB values, 64 KB total
- Query limit: 100 messages (default 20)
