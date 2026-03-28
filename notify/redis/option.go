package redis

import (
	"log/slog"
	"time"
)

// Default configuration values.
const (
	DefaultPrefix       = "notify"
	DefaultMaxLen       = 1000
	DefaultBlockTimeout = 5 * time.Second
	DefaultBackfillSize = 100
)

type options struct {
	prefix       string        // Redis key prefix for notification streams
	maxLen       int64         // Approximate max entries per user stream (MAXLEN ~)
	blockTimeout time.Duration // XREAD BLOCK timeout before retry
	backfillSize int           // Max events replayed on reconnect
	hashTag      bool          // Use Redis Cluster hash tags in keys
	logger       *slog.Logger
}

// Option configures a Redis notification store.
type Option func(*options)

func newOptions(opts ...Option) *options {
	o := &options{
		prefix:       DefaultPrefix,
		maxLen:       DefaultMaxLen,
		blockTimeout: DefaultBlockTimeout,
		backfillSize: DefaultBackfillSize,
		logger:       slog.Default(),
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

// WithLogger sets a custom logger.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}
