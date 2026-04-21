package redis

import (
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// Default configuration values.
const (
	DefaultPrefix          = "notify"
	DefaultMaxLen          = 1000
	DefaultBlockTimeout    = 5 * time.Second
	DefaultBackfillSize    = 100
	DefaultCleanupInterval = 24 * time.Hour        // scan for stale streams daily
	DefaultMaxAge          = 30 * 24 * time.Hour   // delete streams inactive for 30 days
)

type options struct {
	prefix       string        // Redis key prefix for notification streams
	maxLen       int64         // Approximate max entries per user stream (MAXLEN ~)
	blockTimeout time.Duration // XREAD BLOCK timeout before retry
	backfillSize int           // Max events replayed on reconnect
	hashTag      bool          // Use Redis Cluster hash tags in keys
	logger       *slog.Logger
	meter        metric.MeterProvider // Optional meter provider for OTel metrics

	// Background cleanup
	cleanupInterval time.Duration // How often to scan and clean stale streams (0 = disabled)
	maxAge          time.Duration // Streams with no entry newer than this are deleted (0 = disabled)
}

// Option configures a Redis notification store.
type Option func(*options)

func newOptions(opts ...Option) *options {
	o := &options{
		prefix:          DefaultPrefix,
		maxLen:          DefaultMaxLen,
		blockTimeout:    DefaultBlockTimeout,
		backfillSize:    DefaultBackfillSize,
		cleanupInterval: DefaultCleanupInterval,
		maxAge:          DefaultMaxAge,
		logger:          slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// WithPrefix sets the Redis key prefix for notification streams.
// Default is "notify". Keys are formatted as "{prefix}:{userID}".
func WithPrefix(prefix string) Option {
	return func(o *options) {
		if prefix != "" {
			o.prefix = prefix
		}
	}
}

// WithMaxLen sets the approximate max length for each user's stream.
// Uses MAXLEN ~ on XADD for automatic trimming. Default is 1000.
func WithMaxLen(n int64) Option {
	return func(o *options) {
		if n > 0 {
			o.maxLen = n
		}
	}
}

// WithBlockTimeout sets the XREAD BLOCK timeout for live streaming.
// The stream retries after each timeout, checking for context cancellation.
// Default is 5 seconds.
func WithBlockTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.blockTimeout = d
		}
	}
}

// WithBackfillSize caps the number of events replayed on reconnect.
// Default is 100.
func WithBackfillSize(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.backfillSize = n
		}
	}
}

// WithHashTag enables Redis Cluster hash tags in stream keys.
// When enabled, keys are formatted as "{prefix}:{userID}" so that each
// user's stream is deterministically assigned to a cluster slot based on
// the user ID. This ensures SCAN-based cleanup works per-shard.
// Default is false (no hash tags).
func WithHashTag(enabled bool) Option {
	return func(o *options) {
		o.hashTag = enabled
	}
}

// WithCleanupInterval sets how often the store scans for stale notification
// streams and deletes them. Default is 24h. Set to -1 to disable background
// cleanup (Cleanup can still be called manually).
func WithCleanupInterval(d time.Duration) Option {
	return func(o *options) {
		if d == -1 {
			o.cleanupInterval = 0
		} else if d > 0 {
			o.cleanupInterval = d
		}
	}
}

// WithMaxAge sets the retention period for notification streams.
// Streams with no entry newer than maxAge are deleted entirely on the next
// cleanup run; active streams have entries older than maxAge trimmed via
// XTRIM MINID. Empty streams are always deleted.
// Default is 30 days. Set to -1 to disable age-based deletion (only empty
// streams are cleaned up).
func WithMaxAge(d time.Duration) Option {
	return func(o *options) {
		if d == -1 {
			o.maxAge = 0
		} else if d > 0 {
			o.maxAge = d
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

// WithMeterProvider sets the OpenTelemetry meter provider for the store.
// When provided, the store records mailbox.notify.stream.keys and
// mailbox.notify.stream.cleanup.deleted metrics during cleanup runs.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *options) {
		if mp != nil {
			o.meter = mp
		}
	}
}
