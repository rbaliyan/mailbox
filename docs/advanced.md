# Advanced Configuration

## Plugin System

Plugins hook into message sending for validation, spam filtering, or rate limiting:

```go
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

func (p *SpamFilter) AfterSend(ctx context.Context, userID string, msg store.Message) error {
    log.Printf("Message %s sent by %s", msg.GetID(), userID)
    return nil
}

// Register the plugin
svc, _ := mailbox.New(mailbox.Config{},
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
svc, _ := mailbox.New(mailbox.Config{},
    mailbox.WithStore(store),
    mailbox.WithOTel(true),                // Enable both tracing and metrics
    mailbox.WithServiceName("my-mailbox"), // Custom service name
)

// Or enable individually
svc, _ := mailbox.New(mailbox.Config{},
    mailbox.WithStore(store),
    mailbox.WithTracing(true), // Tracing only
    mailbox.WithMetrics(true), // Metrics only
)
```

Tracked metrics:
- `mailbox.messages.sent` - Messages sent counter
- `mailbox.messages.received` - Messages received counter
- `mailbox.operations.duration` - Operation latency histogram

## Message Limits

All limits live on the `Config` struct passed to `mailbox.New`. Refer to the
[godoc for Config](https://pkg.go.dev/github.com/rbaliyan/mailbox#Config) for the
full list and defaults. The most commonly adjusted fields:

```go
svc, _ := mailbox.New(mailbox.Config{
    MaxBodySize:       5 * 1024 * 1024,  // Default: 10 MB
    MaxAttachmentSize: 10 * 1024 * 1024, // Default: 25 MB
    MaxRecipientCount: 50,               // Default: 100
},
    mailbox.WithStore(store),
)
```

Default limits:
- Subject: 998 characters (RFC 5322)
- Attachment count: 20 per message
- Metadata: 64 KB, 100 keys max
- Headers: 50 max, 128-byte keys, 8 KB values, 64 KB total
- Query limit: 100 messages (default 20)
