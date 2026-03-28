# Real-Time Events

Mailbox publishes message lifecycle events to Redis Streams, NATS, Kafka, or any transport supported by the [event](https://github.com/rbaliyan/event) library.

Events are optional — they use a no-op transport by default when no Redis client or event transport is provided. This allows the library to work without any event infrastructure.

## Setting Up Events

Events are automatically registered during `Connect()`. Pass a Redis client to enable Redis Streams transport:

```go
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithRedisClient(redisClient), // enables Redis Streams event transport
)
svc.Connect(ctx)
```

Or use a custom transport:

```go
svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithEventTransport(myTransport),
)
```

## Subscribing to Events

Use per-service events via `svc.Events()`:

```go
// Message received — primary event for recipient notifications
svc.Events().MessageReceived.Subscribe(ctx, func(ctx context.Context, _ event.Event[mailbox.MessageReceivedEvent], data mailbox.MessageReceivedEvent) error {
    log.Printf("Message %s received by %s from %s", data.MessageID, data.RecipientID, data.SenderID)
    return nil
})

// Message sent — fired once per send with all recipients
svc.Events().MessageSent.Subscribe(ctx, func(ctx context.Context, _ event.Event[mailbox.MessageSentEvent], data mailbox.MessageSentEvent) error {
    log.Printf("Message %s sent by %s to %v", data.MessageID, data.SenderID, data.RecipientIDs)
    return nil
})

// Message read
svc.Events().MessageRead.Subscribe(ctx, func(ctx context.Context, _ event.Event[mailbox.MessageReadEvent], data mailbox.MessageReadEvent) error {
    log.Printf("Message %s read by %s", data.MessageID, data.UserID)
    return nil
})

// Message deleted (permanent deletions only, not moves to trash)
svc.Events().MessageDeleted.Subscribe(ctx, func(ctx context.Context, _ event.Event[mailbox.MessageDeletedEvent], data mailbox.MessageDeletedEvent) error {
    log.Printf("Message %s permanently deleted by %s", data.MessageID, data.UserID)
    return nil
})

// Message moved between folders (including archive, trash, restore)
svc.Events().MessageMoved.Subscribe(ctx, func(ctx context.Context, _ event.Event[mailbox.MessageMovedEvent], data mailbox.MessageMovedEvent) error {
    log.Printf("Message %s moved from %s to %s", data.MessageID, data.FromFolderID, data.ToFolderID)
    return nil
})

// Mark all read in a folder
svc.Events().MarkAllRead.Subscribe(ctx, func(ctx context.Context, _ event.Event[mailbox.MarkAllReadEvent], data mailbox.MarkAllReadEvent) error {
    log.Printf("Marked %d messages read in %s for %s", data.Count, data.FolderID, data.UserID)
    return nil
})
```

## Available Events

| Event | Type | Description |
|-------|------|-------------|
| `MessageSent` | `MessageSentEvent` | Message sent (includes all recipient IDs) |
| `MessageReceived` | `MessageReceivedEvent` | Message delivered to a recipient (one per recipient) |
| `MessageRead` | `MessageReadEvent` | Message marked as read |
| `MessageDeleted` | `MessageDeletedEvent` | Message permanently deleted |
| `MessageMoved` | `MessageMovedEvent` | Message moved between folders |
| `MarkAllRead` | `MarkAllReadEvent` | All messages in a folder marked as read |

## Event Payloads

```go
type MessageSentEvent struct {
    MessageID    string    `json:"message_id"`
    SenderID     string    `json:"sender_id"`
    RecipientIDs []string  `json:"recipient_ids"`
    Subject      string    `json:"subject"`
    SentAt       time.Time `json:"sent_at"`
}

type MessageReceivedEvent struct {
    MessageID   string    `json:"message_id"`
    RecipientID string    `json:"recipient_id"`
    SenderID    string    `json:"sender_id"`
    Subject     string    `json:"subject"`
    ReceivedAt  time.Time `json:"received_at"`
}

type MessageReadEvent struct {
    MessageID string    `json:"message_id"`
    UserID    string    `json:"user_id"`
    FolderID  string    `json:"folder_id"`
    ReadAt    time.Time `json:"read_at"`
}

type MessageDeletedEvent struct {
    MessageID string    `json:"message_id"`
    UserID    string    `json:"user_id"`
    FolderID  string    `json:"folder_id"`
    WasUnread bool      `json:"was_unread"`
    DeletedAt time.Time `json:"deleted_at"`
}

type MessageMovedEvent struct {
    MessageID    string    `json:"message_id"`
    UserID       string    `json:"user_id"`
    FromFolderID string    `json:"from_folder_id"`
    ToFolderID   string    `json:"to_folder_id"`
    WasUnread    bool      `json:"was_unread"`
    MovedAt      time.Time `json:"moved_at"`
}

type MarkAllReadEvent struct {
    UserID   string    `json:"user_id"`
    FolderID string    `json:"folder_id"`
    Count    int64     `json:"count"`
    MarkedAt time.Time `json:"marked_at"`
}
```
