// Package redis provides a Redis-backed distributed stats cache for mailbox.
package redis

import (
	"log/slog"
	"time"
)

const (
	defaultTTL    = 5 * time.Minute
	defaultPrefix = "mailbox:stats"
)

// options holds Redis stats cache configuration.
type options struct {
	prefix string
	ttl    time.Duration
	logger *slog.Logger
}

// Option configures the Redis stats cache.
type Option func(*options)

// WithPrefix sets the Redis key prefix. Default is "mailbox:stats".
func WithPrefix(prefix string) Option {
	return func(o *options) {
		if prefix != "" {
			o.prefix = prefix
		}
	}
}

// WithTTL sets the cache entry TTL. Default is 5 minutes.
func WithTTL(ttl time.Duration) Option {
	return func(o *options) {
		if ttl > 0 {
			o.ttl = ttl
		}
	}
}

// WithLogger sets a custom logger.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		if logger != nil {
			o.logger = logger
		}
	}
}
