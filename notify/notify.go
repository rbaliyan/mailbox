package notify

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors for the notify package.
var (
	// ErrStreamClosed is returned when reading from a closed stream.
	ErrStreamClosed = errors.New("notify: stream closed")

	// ErrNotifierClosed is returned when the notifier has been closed.
	ErrNotifierClosed = errors.New("notify: notifier closed")

	// ErrStoreClosed is returned when the notification store has been closed.
	ErrStoreClosed = errors.New("notify: store closed")
)

// Event is a notification delivered to a user's stream.
type Event struct {
	// ID is the store-assigned event ID, used for Last-Event-ID resume.
	ID string `json:"id"`

	// Type identifies the event kind (e.g., "mailbox.message.received").
	Type string `json:"type"`

	// UserID is the target user for this notification.
	UserID string `json:"user_id"`

	// Payload is the JSON-encoded event-specific data.
	Payload []byte `json:"payload"`

	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"timestamp"`
}

// Stream is a per-user notification stream.
// Callers read events by calling Next in a loop until the context is
// cancelled or the stream is closed.
type Stream interface {
	// Next blocks until the next event is available or ctx is cancelled.
	// Returns ErrStreamClosed if the stream has been closed.
	Next(ctx context.Context) (Event, error)

	// Close releases resources associated with this stream.
	// After Close, Next returns ErrStreamClosed.
	Close() error
}

// Router delivers notification events to remote instances.
// When presence tracking includes routing information, the notifier uses a
// Router to forward events to the instance holding the user's connection,
// avoiding store polling latency.
//
// Implementations might use HTTP, gRPC, Redis Pub/Sub, or any other
// inter-instance communication mechanism.
type Router interface {
	// Route delivers an event to the instance identified by the routing info.
	// Returning an error causes the notifier to fall back to store persistence
	// (the remote instance will pick it up via polling).
	Route(ctx context.Context, info RoutingInfo, evt Event) error
}

// RoutingInfo describes where to deliver a notification.
// Mirrors presence.RoutingInfo but decoupled to avoid a dependency from
// Router implementations back to the presence package.
type RoutingInfo struct {
	// InstanceID identifies the target server instance.
	InstanceID string `json:"instance_id,omitempty"`
	// Metadata holds arbitrary routing data (e.g., address, port).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Store persists notifications per user for backfill on reconnect.
// Implementations must be safe for concurrent use.
type Store interface {
	// Save persists a notification event. The implementation assigns the event ID.
	Save(ctx context.Context, evt *Event) error

	// List returns notifications for a user after the given event ID, ordered
	// by ID ascending. Pass "" for afterID to list from the beginning.
	// Limit caps the number of returned events (0 for implementation default).
	List(ctx context.Context, userID string, afterID string, limit int) ([]Event, error)

	// Cleanup removes notifications older than the given time.
	Cleanup(ctx context.Context, olderThan time.Time) error

	// Close releases resources held by the store.
	Close(ctx context.Context) error
}

// StreamStore is an optional interface that Store implementations can
// implement when they support native event streaming. When the notifier's
// store implements StreamStore, Subscribe delegates to the store's native
// streaming instead of using channel-based delivery with polling.
//
// Redis Streams is the canonical example: XADD persists events and
// XREAD BLOCK delivers them, collapsing store and stream into a single
// data structure — no channels, no poll loops, no Router needed.
type StreamStore interface {
	Store

	// Subscribe returns a Stream backed by native streaming (e.g., XREAD BLOCK).
	// If lastEventID is non-empty, missed events are replayed before live delivery.
	Subscribe(ctx context.Context, userID string, lastEventID string) (Stream, error)
}
