package rules

import "log/slog"

type engineOptions struct {
	logger       *slog.Logger
	strictMode   bool
	maxCacheSize int
}

// Option configures the rules Engine.
type Option func(*engineOptions)

// WithLogger sets the logger for the rules engine.
func WithLogger(l *slog.Logger) Option {
	return func(o *engineOptions) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithStrictMode makes rule evaluation errors fatal.
// When enabled, CEL compilation or evaluation errors cause the operation to fail
// instead of being logged and skipped.
func WithStrictMode(strict bool) Option {
	return func(o *engineOptions) {
		o.strictMode = strict
	}
}

// WithMaxCacheSize sets the maximum number of compiled CEL programs to cache.
// When the cache is full, all entries are evicted. Defaults to DefaultMaxCacheSize (1000).
// Set to 0 to disable caching.
func WithMaxCacheSize(size int) Option {
	return func(o *engineOptions) {
		o.maxCacheSize = size
	}
}
