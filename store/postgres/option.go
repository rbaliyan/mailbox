package postgres

import (
	"log/slog"
	"time"
)

// Default configuration values.
const (
	DefaultTable   = "messages"
	DefaultTimeout = 10 * time.Second
)

// options holds PostgreSQL store configuration.
type options struct {
	table   string
	timeout time.Duration
	logger  *slog.Logger
}

func newOptions(opts ...Option) *options {
	o := &options{
		table:   DefaultTable,
		timeout: DefaultTimeout,
		logger:  slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Option configures a PostgreSQL store.
type Option func(*options)

// WithTable sets the table name.
func WithTable(name string) Option {
	return func(o *options) {
		if name != "" {
			o.table = name
		}
	}
}

// WithTimeout sets the operation timeout.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.timeout = d
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
