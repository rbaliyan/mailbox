# SSE Client Guide: Ordering and Gap Detection

Mailbox notification streams deliver events via Server-Sent Events (SSE). Each event carries an opaque `id` and a `timestamp` that together enable ordering, gap detection, and reliable reconnection.

## Event Format

```
id: <opaque-event-id>
event: mailbox.message.received
data: {"message_id":"abc","sender_id":"alice","subject":"Hello","received_at":"2025-03-28T10:00:00Z"}

id: <opaque-event-id>
event: mailbox.message.read
data: {"message_id":"abc","user_id":"bob","read_at":"2025-03-28T10:01:00Z"}
```

**Fields:**
- `id` — Opaque, server-assigned event identifier. Guaranteed to be **monotonically increasing** within a user's stream. Safe to compare lexicographically for ordering.
- `event` — Event type (see table below).
- `data` — JSON payload with event-specific fields. Always includes a timestamp field (`received_at`, `read_at`, `sent_at`, etc.).

## Event Types

| Type | Description | Key Payload Fields |
|------|-------------|--------------------|
| `mailbox.message.sent` | Message sent by this user | `message_id`, `sender_id`, `recipient_ids`, `sent_at` |
| `mailbox.message.received` | Message delivered to this user | `message_id`, `sender_id`, `subject`, `received_at` |
| `mailbox.message.read` | Message marked as read | `message_id`, `user_id`, `read_at` |
| `mailbox.message.deleted` | Message permanently deleted | `message_id`, `user_id`, `deleted_at` |
| `mailbox.message.moved` | Message moved between folders | `message_id`, `from_folder_id`, `to_folder_id`, `moved_at` |
| `mailbox.mark_all_read` | All messages in folder marked read | `user_id`, `folder_id`, `count`, `marked_at` |

## Ordering

Events are delivered in order under normal conditions. During reconnection or server failover, events may arrive slightly out of order.

**Use the event `id` for ordering, not payload timestamps.** Event IDs are server-assigned and monotonically increasing. Lexicographic string comparison produces correct chronological order.

```javascript
const events = [];

eventSource.addEventListener('mailbox.message.received', (e) => {
  const event = { id: e.lastEventId, data: JSON.parse(e.data) };

  // Fast path: most events arrive in order.
  if (events.length === 0 || e.lastEventId > events[events.length - 1].id) {
    events.push(event);
  } else {
    // Out-of-order: insert at correct position.
    const idx = events.findIndex(ev => ev.id > e.lastEventId);
    events.splice(idx, 0, event);
  }

  renderEvents(events);
});
```

## Reconnection

The SSE protocol handles reconnection automatically. The browser resends the last received event ID via the `Last-Event-ID` header, and the server replays missed events before resuming live delivery.

```javascript
// Browser handles reconnection and Last-Event-ID automatically.
const es = new EventSource('/api/v1/users/my/mailbox/notifications');
```

For custom clients (mobile apps, non-browser):

```javascript
let lastEventID = '';

function connect() {
  const url = lastEventID
    ? `/api/v1/users/my/mailbox/notifications?last_event_id=${lastEventID}`
    : '/api/v1/users/my/mailbox/notifications';

  const es = new EventSource(url);

  es.onmessage = (e) => {
    lastEventID = e.lastEventId;
    handleEvent(e);
  };

  es.onerror = () => {
    es.close();
    setTimeout(connect, 3000);
  };
}
```

The server replays up to 100 missed events (configurable). If more events were missed, the client should fall back to the REST API.

## Deduplication

During reconnection, the server may replay events the client already received. Deduplicate using the event `id`:

```javascript
const seen = new Set();

function handleEvent(e) {
  if (seen.has(e.lastEventId)) return;
  seen.add(e.lastEventId);

  // Prune old IDs to bound memory.
  if (seen.size > 1000) {
    const sorted = [...seen].sort();
    for (const id of sorted.slice(0, 500)) {
      seen.delete(id);
    }
  }

  processEvent(e);
}
```

## Gap Detection and Recovery

If the client suspects missed events (e.g., long disconnection, server reports backfill limit exceeded), recover by fetching the full inbox via REST:

```javascript
async function fullSync() {
  const response = await fetch('/api/v1/users/my/mailbox/inbox');
  const inbox = await response.json();
  reconcileLocalState(inbox.messages);
}
```

**When to trigger a full sync:**
- SSE connection was down longer than the server's backfill window
- Client detects unexpected state (e.g., unread count mismatch)
- Application returns from background/sleep

## Recommended Client Architecture

```
┌──────────────────────────────────────────┐
│  SSE Connection                          │
│  GET /users/my/mailbox/notifications     │
│                                          │
│  ┌─────────────┐    ┌────────────────┐   │
│  │ Dedup by ID  │──>│ Sort by ID     │   │
│  └─────────────┘    └────────────────┘   │
│                            │             │
│                     ┌──────▼──────┐      │
│                     │ Event Store │      │
│                     │ (local)     │      │
│                     └──────┬──────┘      │
│                            │             │
│                     ┌──────▼──────┐      │
│                     │ UI Update   │      │
│                     └─────────────┘      │
│                                          │
│  On reconnect failure or long gap:       │
│  GET /users/my/mailbox/inbox → full sync │
└──────────────────────────────────────────┘
```

## Summary

| Concern | Mechanism |
|---------|-----------|
| Ordering | Compare event `id` lexicographically |
| Reconnection | `Last-Event-ID` header (automatic in browsers) |
| Backfill | Server replays missed events on reconnect |
| Deduplication | Track seen event IDs in a bounded set |
| Full recovery | Fetch inbox via REST API |
