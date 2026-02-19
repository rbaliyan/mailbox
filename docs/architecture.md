# Architecture

## Addressable, Not Topic-Based

Unlike message brokers where you publish to topics and consumers subscribe, Mailbox delivers to named recipients. Each recipient gets an independent copy with its own state. This is the same model as email (IMAP/JMAP) — but as a library, without protocol overhead.

```
Producer ──send──> Mailbox Store ──query──> Consumer
                   (persists)               (reads when ready)
```

Producers and consumers are temporally decoupled. Messages persist in the store and can be queried, filtered, and managed by the recipient at any point. This makes it natural for async workflows where the consumer may not be running when the message is sent.

## No Distributed Locks

All concurrency is handled through database-native atomicity:

1. **Atomic Database Operations** - MongoDB `findOneAndUpdate`, PostgreSQL `INSERT ON CONFLICT`
2. **Idempotent Delivery** - Per-recipient deduplication keys prevent duplicates on retry
3. **Transactional Batches** - Database transactions for multi-document atomicity

## Store Interface

The store is composed of four sub-interfaces:

- **DraftStore** - Mutable draft operations
- **MessageStore** - Message queries and state mutations
- **MaintenanceStore** - Background maintenance operations
- **StatsStore** - Aggregate mailbox statistics

## Service vs Client

- **Service** - Singleton that manages connections, configuration, and shared resources
- **Client** - Lightweight, per-identity handle (obtained via `svc.Client(id)`) — works for users and services alike
