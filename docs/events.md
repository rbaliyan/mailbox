# Real-Time Events

Mailbox can publish message lifecycle events to Redis Streams, NATS, Kafka, or any transport supported by the [event](https://github.com/rbaliyan/event) library.

Events are optional — they use a no-op transport by default and are silently skipped if `RegisterEvents()` is not called. This allows the library to work without any event infrastructure.

## Setting Up Events

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

## Subscribing to Events

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

## Available Events

| Event | Description |
|-------|-------------|
| `EventMessageSent` | Message was sent (primary event for recipient notifications) |
| `EventMessageRead` | Message was marked as read (for read receipts) |
| `EventMessageDeleted` | Message permanently deleted |
