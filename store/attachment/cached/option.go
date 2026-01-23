package cached

import (
	"log/slog"
	"time"
)

// options holds cached store configuration.
type options struct {
	cacheDir string
	maxSize  int64
	ttl      time.Duration
	logger   *slog.Logger
}

// Option configures the cached store.
type Option func(*options)

// WithCacheDir sets the directory for cached files.
// Default is the system temp directory.
func WithCacheDir(dir string) Option {
	return func(o *options) {
		o.cacheDir = dir
	}
}

// WithMaxSize sets the maximum cache size in bytes.
// Default is 1GB. When the cache is full, new files won't be cached
// until old entries expire.
func WithMaxSize(size int64) Option {
	return func(o *options) {
		if size > 0 {
			o.maxSize = size
		}
	}
}

// WithTTL sets the time-to-live for cached files.
// Default is 24 hours. Files older than TTL are removed during cleanup.
// Set to 0 to disable automatic cleanup (not recommended).
func WithTTL(ttl time.Duration) Option {
	return func(o *options) {
		o.ttl = ttl
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
