package redis

import (
	"log/slog"
	"time"
)

// Default configuration values.
const (
	DefaultPrefix     = "presence"
	DefaultTTL        = 30 * time.Second
	DefaultRefreshTTL = 10 * time.Second
)

type options struct {
	prefix    string        // Redis key prefix
	ttl       time.Duration // TTL for presence keys
	refreshAt time.Duration // Refresh interval (must be < ttl)
	logger    *slog.Logger
}

// Option configures a Redis presence tracker.
type Option func(*options)

func newOptions(opts ...Option) *options {
	o := &options{
		prefix:    DefaultPrefix,
		ttl:       DefaultTTL,
		refreshAt: DefaultRefreshTTL,
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	// Ensure refresh is less than TTL.
	if o.refreshAt >= o.ttl {
		o.refreshAt = o.ttl / 3
	}
	return o
}

// WithPrefix sets the Redis key prefix for presence keys.
// Default is "presence".
func WithPrefix(prefix string) Option {
	return func(o *options) {
		if prefix != "" {
			o.prefix = prefix
		}
	}
}

// WithTTL sets the TTL for presence keys.
// Registrations are automatically refreshed before expiry.
// Default is 30 seconds.
func WithTTL(ttl time.Duration) Option {
	return func(o *options) {
		if ttl > 0 {
			o.ttl = ttl
		}
	}
}

// WithRefreshInterval sets how often the presence key TTL is refreshed.
// Must be less than TTL. Default is 10 seconds.
func WithRefreshInterval(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.refreshAt = d
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
