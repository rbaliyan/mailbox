package rules

import (
	"log/slog"
	"net/http"
)

type engineOptions struct {
	logger       *slog.Logger
	strictMode   bool
	maxCacheSize int
	forwarder    Forwarder    // optional: enables ActionForward
	httpClient   *http.Client // optional: used by ActionWebhook
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

// WithForwarder sets the forwarder used by ActionForward rules.
// Without a forwarder, ActionForward is logged and skipped.
func WithForwarder(f Forwarder) Option {
	return func(o *engineOptions) {
		if f != nil {
			o.forwarder = f
		}
	}
}

// WithHTTPClient sets the HTTP client used by ActionWebhook rules.
// Defaults to http.DefaultClient when not set.
func WithHTTPClient(c *http.Client) Option {
	return func(o *engineOptions) {
		if c != nil {
			o.httpClient = c
		}
	}
}
