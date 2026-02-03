package postgres

import (
	"log/slog"
	"regexp"
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

// validIdentifier matches safe SQL identifier names (letters, digits, underscores).
var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// WithTable sets the table name.
// The name must be a valid SQL identifier (letters, digits, underscores only)
// since it is interpolated into queries as an identifier.
func WithTable(name string) Option {
	return func(o *options) {
		if name != "" && validIdentifier.MatchString(name) {
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
