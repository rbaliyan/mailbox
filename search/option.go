package search

import "log/slog"

// options holds plugin configuration.
type options struct {
	// fallback enables falling back to the primary store's Search on provider error.
	fallback bool
	logger   *slog.Logger
}

// Option configures the search Plugin.
type Option func(*options)

// WithFallback controls whether a search provider error causes a fallback to
// the primary store's Search. Defaults to true.
func WithFallback(enabled bool) Option {
	return func(o *options) {
		o.fallback = enabled
	}
}

// WithLogger sets the logger used by the plugin. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}
