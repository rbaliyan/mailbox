package notify

import (
	"log/slog"
	"time"

	"github.com/rbaliyan/mailbox/presence"
)

// Default configuration values.
const (
	DefaultPollInterval = 2 * time.Second
	DefaultBufferSize   = 64
)

type options struct {
	store        Store
	presence     presence.Tracker
	router       Router // Optional cross-instance delivery
	instanceID   string // This instance's ID, for comparing with presence routing
	pollInterval time.Duration // How often streams poll the store for events from other instances
	bufferSize   int           // Channel buffer size for local delivery
	logger       *slog.Logger
}

// Option configures a Notifier.
type Option func(*options)

func newOptions(opts ...Option) *options {
	o := &options{
		pollInterval: DefaultPollInterval,
		bufferSize:   DefaultBufferSize,
		logger:       slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// WithStore sets the notification store for persistence and backfill.
func WithStore(s Store) Option {
	return func(o *options) {
		if s != nil {
			o.store = s
		}
	}
}

// WithPresence sets the presence tracker.
// When provided, Push skips offline users entirely (no store write, no delivery).
func WithPresence(t presence.Tracker) Option {
	return func(o *options) {
		if t != nil {
			o.presence = t
		}
	}
}

// WithPollInterval sets how often streams poll the store for events
// that were written by other instances. Default is 2 seconds.
func WithPollInterval(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.pollInterval = d
		}
	}
}

// WithBufferSize sets the channel buffer size for local event delivery.
// Default is 64.
func WithBufferSize(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.bufferSize = n
		}
	}
}

// WithRouter sets the cross-instance event router.
// When both a Router and Presence (with routing info) are configured,
// the notifier routes events directly to the instance holding the user's
// connection instead of relying solely on store polling.
func WithRouter(r Router) Option {
	return func(o *options) {
		if r != nil {
			o.router = r
		}
	}
}

// WithInstanceID sets this instance's identifier.
// Used to compare with presence routing info to determine whether a user
// is connected locally or on a remote instance.
func WithInstanceID(id string) Option {
	return func(o *options) {
		if id != "" {
			o.instanceID = id
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
