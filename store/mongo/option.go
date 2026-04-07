package mongo

import (
	"log/slog"
	"time"
)

// Default configuration values.
const (
	DefaultDatabase   = "mailbox"
	DefaultCollection = "messages"
	DefaultTimeout    = 10 * time.Second
)

// options holds MongoDB store configuration.
type options struct {
	database         string
	collection       string
	outboxCollection string // collection for outbox events (default: "outbox")
	timeout          time.Duration
	logger           *slog.Logger
	enableRegex      bool // Enable regex-based text search (disabled by default for security)
	outboxEnabled    bool // Enable transactional outbox for atomic event persistence
}

func newOptions(opts ...Option) *options {
	o := &options{
		database:         DefaultDatabase,
		collection:       DefaultCollection,
		outboxCollection: "outbox",
		timeout:          DefaultTimeout,
		logger:           slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Option configures a MongoDB store.
type Option func(*options)

// WithDatabase sets the database name.
func WithDatabase(name string) Option {
	return func(o *options) {
		if name != "" {
			o.database = name
		}
	}
}

// WithCollection sets the collection name.
func WithCollection(name string) Option {
	return func(o *options) {
		if name != "" {
			o.collection = name
		}
	}
}

// WithTimeout sets the operation timeout.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// WithLogger sets a custom logger.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithOutbox enables transactional outbox for atomic event persistence.
// When enabled, mutation methods wrap DB writes in a MongoDB transaction and
// set event.WithOutboxTx on the context so the event bus auto-routes
// Event.Publish() calls to the outbox collection in the same transaction.
// A background relay (outbox.Relay) reads from the outbox and publishes to the event transport.
func WithOutbox(enabled bool) Option {
	return func(o *options) {
		o.outboxEnabled = enabled
	}
}

// WithOutboxCollection sets the outbox collection name. Default is "outbox".
func WithOutboxCollection(name string) Option {
	return func(o *options) {
		if name != "" {
			o.outboxCollection = name
		}
	}
}

// WithEnableRegex enables regex-based text search.
// By default, regex search is disabled for security reasons (ReDoS prevention).
// When disabled, text search queries will return ErrRegexSearchDisabled.
// Enable this only if you trust the search input or have proper rate limiting.
func WithEnableRegex(enable bool) Option {
	return func(o *options) {
		o.enableRegex = enable
	}
}
