package server

import (
	"log/slog"
	"time"
)

// serverOptions holds the configuration for a Server instance.
type serverOptions struct {
	guard               SecurityGuard
	logger              *slog.Logger
	maxBulkSize         int
	streamMaxDuration   time.Duration // 0 means no limit
}

func defaultOptions() *serverOptions {
	return &serverOptions{
		guard:       DenyAll(),
		logger:      slog.Default(),
		maxBulkSize: 500,
	}
}

// Option configures a Server.
type Option func(*serverOptions)

// WithSecurityGuard sets the SecurityGuard used for authentication and
// authorization. If g is nil the option is ignored and the default DenyAll
// guard is kept.
func WithSecurityGuard(g SecurityGuard) Option {
	return func(o *serverOptions) {
		if g != nil {
			o.guard = g
		}
	}
}

// WithLogger sets the structured logger used by the server. If l is nil the
// option is ignored.
func WithLogger(l *slog.Logger) Option {
	return func(o *serverOptions) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithMaxBulkSize sets the maximum number of message IDs accepted in a single
// bulk request (BulkDelete, BulkMove, etc.). Requests exceeding this limit are
// rejected with codes.InvalidArgument. Default: 500.
func WithMaxBulkSize(n int) Option {
	return func(o *serverOptions) {
		if n > 0 {
			o.maxBulkSize = n
		}
	}
}

// WithStreamMaxDuration sets a hard upper bound on how long a
// StreamNotifications stream may remain open. After the duration elapses the
// stream is closed with a normal EOF, forcing clients to reconnect. This
// prevents half-open connections from holding goroutines indefinitely.
// A zero or negative duration disables the limit (default: no limit).
func WithStreamMaxDuration(d time.Duration) Option {
	return func(o *serverOptions) {
		if d > 0 {
			o.streamMaxDuration = d
		}
	}
}
