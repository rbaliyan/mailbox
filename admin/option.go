package admin

import (
	"log/slog"
	"net/http"
)

// options holds Handler configuration.
type options struct {
	logger   *slog.Logger
	authFunc func(r *http.Request) bool
}

// Option configures a Handler.
type Option func(*options)

// WithLogger sets a custom logger. Default is slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithAuthFunc sets an authorization function. When set, each request is passed
// to the function before handling; a false return results in a 403 response.
// When nil (default), all requests are allowed.
func WithAuthFunc(fn func(r *http.Request) bool) Option {
	return func(o *options) {
		o.authFunc = fn
	}
}
