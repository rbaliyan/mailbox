package mailbox

import (
	"log/slog"
	"time"

	"github.com/rbaliyan/event/v3/transport"
	"github.com/rbaliyan/mailbox/store"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Default configuration values.
const (
	DefaultTrashRetention  = 30 * 24 * time.Hour // 30 days
	MinTrashRetention      = 24 * time.Hour      // 1 day minimum
	DefaultShutdownTimeout = 30 * time.Second    // default graceful shutdown timeout
	MinShutdownTimeout     = 1 * time.Second     // minimum shutdown timeout

	// Default message limits
	DefaultMaxSubjectLength   = 998               // RFC 5322 max line length
	DefaultMaxBodySize        = 10 * 1024 * 1024  // 10 MB
	DefaultMaxAttachmentSize  = 25 * 1024 * 1024  // 25 MB per attachment
	DefaultMaxAttachmentCount = 20                // max attachments per message
	DefaultMaxRecipientCount  = 100               // max recipients per message
	DefaultMaxMetadataSize    = 64 * 1024         // 64 KB total metadata
	DefaultMaxMetadataKeys    = 100               // max metadata keys

	// Query limits
	DefaultMaxQueryLimit = 100 // max messages per query
	DefaultQueryLimit    = 20  // default messages per query

	// Concurrency limits
	DefaultMaxConcurrentSends = 10 // max concurrent send operations per service

	// Stats cache
	DefaultStatsRefreshInterval = 30 * time.Second // TTL for cached stats
)

// options holds mailbox configuration.
type options struct {
	store       store.Store
	attachments store.AttachmentManager
	logger      *slog.Logger

	plugins []Plugin

	// Trash cleanup configuration (for manual cleanup via CleanupTrash method)
	trashRetention time.Duration

	// Message limits
	maxSubjectLength   int
	maxBodySize        int
	maxAttachmentSize  int64
	maxAttachmentCount int
	maxRecipientCount  int
	maxMetadataSize    int
	maxMetadataKeys    int

	// Query limits
	maxQueryLimit     int
	defaultQueryLimit int

	// Concurrency limits
	maxConcurrentSends int

	// Shutdown
	shutdownTimeout time.Duration

	// OpenTelemetry
	tracingEnabled bool
	metricsEnabled bool
	serviceName    string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider

	// Stats cache
	statsRefreshInterval time.Duration // TTL for cached stats

	// Event handling
	eventErrorsFatal       bool                    // If true, event publishing failures cause operation to fail
	eventTransport         transport.Transport     // Event transport (optional, uses noop if nil)
	redisClient            redis.UniversalClient   // Redis client for event transport (optional, uses noop if nil)
	onEventPublishFailure  EventPublishFailureFunc // Callback for event publish failures (always set)
}

// EventPublishFailureFunc is called when an event fails to publish.
// The eventName is the name of the event (e.g., "MessageSent"), and err is the publish error.
type EventPublishFailureFunc func(eventName string, err error)

// safeEventPublishFailure calls the event failure callback with panic recovery.
// If the callback panics, the panic is logged and suppressed to prevent cascading failures.
func (o *options) safeEventPublishFailure(eventName string, err error) {
	if o.onEventPublishFailure == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			o.logger.Error("panic in event publish failure handler",
				"event", eventName,
				"original_error", err,
				"panic", r,
			)
		}
	}()
	o.onEventPublishFailure(eventName, err)
}

// newOptions creates options with defaults and applies provided options.
func newOptions(opts ...Option) *options {
	o := &options{
		logger: slog.Default(),
		trashRetention: DefaultTrashRetention,
		// Message limits defaults
		maxSubjectLength:   DefaultMaxSubjectLength,
		maxBodySize:        DefaultMaxBodySize,
		maxAttachmentSize:  DefaultMaxAttachmentSize,
		maxAttachmentCount: DefaultMaxAttachmentCount,
		maxRecipientCount:  DefaultMaxRecipientCount,
		maxMetadataSize:    DefaultMaxMetadataSize,
		maxMetadataKeys:    DefaultMaxMetadataKeys,
		// Query limits defaults
		maxQueryLimit:     DefaultMaxQueryLimit,
		defaultQueryLimit: DefaultQueryLimit,
		// Concurrency limits defaults
		maxConcurrentSends: DefaultMaxConcurrentSends,
		// Shutdown defaults
		shutdownTimeout: DefaultShutdownTimeout,
		// Stats cache defaults
		statsRefreshInterval: DefaultStatsRefreshInterval,
	}
	for _, opt := range opts {
		opt(o)
	}

	// Validate query limits consistency
	if o.defaultQueryLimit > o.maxQueryLimit {
		o.defaultQueryLimit = o.maxQueryLimit
	}

	// Ensure event failure callback is always set
	if o.onEventPublishFailure == nil {
		o.onEventPublishFailure = func(eventName string, err error) {
			o.logger.Error("failed to publish event", "event", eventName, "error", err)
		}
	}

	return o
}

// Option configures a mailbox.
type Option func(*options)

// --- Core Options ---

// WithStore sets the storage backend (required).
func WithStore(s store.Store) Option {
	return func(o *options) {
		if s != nil {
			o.store = s
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

// --- Plugin/Extension Options ---

// WithPlugin registers a plugin with the mailbox service.
// Plugins can hook into message lifecycle events.
// Multiple plugins can be registered by calling this option multiple times.
func WithPlugin(p Plugin) Option {
	return func(o *options) {
		if p != nil {
			o.plugins = append(o.plugins, p)
		}
	}
}

// WithPlugins registers multiple plugins at once.
func WithPlugins(plugins ...Plugin) Option {
	return func(o *options) {
		for _, p := range plugins {
			if p != nil {
				o.plugins = append(o.plugins, p)
			}
		}
	}
}

// WithAttachmentManager sets the attachment manager for reference-counted attachments.
// When provided, attachments are tracked with reference counting and
// automatically deleted when no messages reference them.
func WithAttachmentManager(m store.AttachmentManager) Option {
	return func(o *options) {
		if m != nil {
			o.attachments = m
		}
	}
}

// --- Trash Options ---

// WithTrashRetention sets how long messages stay in trash before cleanup.
// Default is 30 days. Minimum is 1 day.
func WithTrashRetention(d time.Duration) Option {
	return func(o *options) {
		if d >= MinTrashRetention {
			o.trashRetention = d
		}
	}
}

// --- OTel Options ---

// WithTracing enables or disables OpenTelemetry tracing.
// When enabled, spans are created for all mailbox operations.
// Default is disabled.
func WithTracing(enabled bool) Option {
	return func(o *options) {
		o.tracingEnabled = enabled
	}
}

// WithMetrics enables or disables OpenTelemetry metrics.
// When enabled, metrics are collected for all mailbox operations.
// Default is disabled.
func WithMetrics(enabled bool) Option {
	return func(o *options) {
		o.metricsEnabled = enabled
	}
}

// WithOTel enables both OpenTelemetry tracing and metrics.
// This is a convenience function equivalent to calling
// WithTracing(true) and WithMetrics(true).
func WithOTel(enabled bool) Option {
	return func(o *options) {
		o.tracingEnabled = enabled
		o.metricsEnabled = enabled
	}
}

// WithServiceName sets the service name for OpenTelemetry telemetry.
// Default is "mailbox".
func WithServiceName(name string) Option {
	return func(o *options) {
		if name != "" {
			o.serviceName = name
		}
	}
}

// WithTracerProvider sets a custom OpenTelemetry tracer provider.
// Default uses the global tracer provider from otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *options) {
		if tp != nil {
			o.tracerProvider = tp
		}
	}
}

// WithMeterProvider sets a custom OpenTelemetry meter provider.
// Default uses the global meter provider from otel.GetMeterProvider().
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *options) {
		if mp != nil {
			o.meterProvider = mp
		}
	}
}

// --- Message Limit Options ---

// WithMaxBodySize sets the maximum body size in bytes.
// Default is 10 MB.
func WithMaxBodySize(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxBodySize = n
		}
	}
}

// WithMaxAttachmentSize sets the maximum size per attachment in bytes.
// Default is 25 MB.
func WithMaxAttachmentSize(n int64) Option {
	return func(o *options) {
		if n > 0 {
			o.maxAttachmentSize = n
		}
	}
}

// WithMaxRecipients sets the maximum number of recipients per message.
// Default is 100.
func WithMaxRecipients(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxRecipientCount = n
		}
	}
}

// WithMaxSubjectLength sets the maximum subject length in characters.
// Default is 998 (RFC 5322 max line length).
func WithMaxSubjectLength(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxSubjectLength = n
		}
	}
}

// WithMaxAttachmentCount sets the maximum number of attachments per message.
// Default is 20.
func WithMaxAttachmentCount(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxAttachmentCount = n
		}
	}
}

// WithMaxMetadataSize sets the maximum total metadata size in bytes.
// Default is 64 KB.
func WithMaxMetadataSize(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxMetadataSize = n
		}
	}
}

// WithMaxMetadataKeys sets the maximum number of metadata keys per message.
// Default is 100.
func WithMaxMetadataKeys(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxMetadataKeys = n
		}
	}
}

// --- Query Limit Options ---

// WithMaxQueryLimit sets the maximum number of messages per query.
// Any query requesting more than this limit will be capped.
// Default is 100.
func WithMaxQueryLimit(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxQueryLimit = n
		}
	}
}

// WithDefaultQueryLimit sets the default number of messages per query
// when no limit is specified. If this exceeds MaxQueryLimit, it is
// automatically capped to MaxQueryLimit.
// Default is 20.
func WithDefaultQueryLimit(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.defaultQueryLimit = n
		}
	}
}

// --- Concurrency Options ---

// WithMaxConcurrentSends sets the maximum number of concurrent send operations.
// This prevents resource exhaustion when many messages are being sent simultaneously.
// Default is 10.
func WithMaxConcurrentSends(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxConcurrentSends = n
		}
	}
}

// WithShutdownTimeout sets the maximum time to wait for in-flight operations
// during graceful shutdown. When Close() is called, the service waits up to
// this duration for ongoing send operations to complete.
// Default is 30 seconds. Minimum is 1 second.
func WithShutdownTimeout(d time.Duration) Option {
	return func(o *options) {
		if d >= MinShutdownTimeout {
			o.shutdownTimeout = d
		}
	}
}

// --- Stats Options ---

// WithStatsRefreshInterval sets the TTL for cached mailbox stats.
// After this duration, the next Stats() call will refresh from the store.
// Event-driven incremental updates keep the cache approximately correct between refreshes.
// Default is 30 seconds.
func WithStatsRefreshInterval(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.statsRefreshInterval = d
		}
	}
}

// --- Event Options ---

// WithEventErrorsFatal configures whether event publishing failures should
// cause the operation to fail. By default, event failures are logged but
// the operation succeeds (the message is still sent).
//
// Set to true if your application requires guaranteed event delivery,
// for example when events drive critical downstream processes.
// Set to false (default) for fire-and-forget event publishing.
func WithEventErrorsFatal(fatal bool) Option {
	return func(o *options) {
		o.eventErrorsFatal = fatal
	}
}

// WithEventTransport sets the event transport for publishing and subscribing.
// When provided, events are published via the given transport for reliable delivery.
// If not provided, a noop transport is used (events are silently dropped).
//
// Example with Redis:
//
//	transport, _ := redis.New(redisClient)
//	svc, _ := mailbox.NewService(mailbox.WithEventTransport(transport))
func WithEventTransport(t transport.Transport) Option {
	return func(o *options) {
		if t != nil {
			o.eventTransport = t
		}
	}
}

// WithRedisClient sets a Redis client for the event transport.
// When provided, events are published to Redis Streams for reliable delivery.
// If not provided, a noop transport is used (events are silently dropped).
//
// Compatible with *redis.Client, *redis.ClusterClient, and redis.UniversalClient.
func WithRedisClient(client redis.UniversalClient) Option {
	return func(o *options) {
		if client != nil {
			o.redisClient = client
		}
	}
}

// WithEventPublishFailureHandler sets a callback for event publishing failures.
// This callback is invoked whenever an event fails to publish (and eventErrorsFatal is false).
// Use this for custom logging, metrics, or alerting on event failures.
//
// By default, failures are logged using the configured logger.
func WithEventPublishFailureHandler(fn EventPublishFailureFunc) Option {
	return func(o *options) {
		if fn != nil {
			o.onEventPublishFailure = fn
		}
	}
}

// getLimits returns the configured message limits.
func (o *options) getLimits() MessageLimits {
	return MessageLimits{
		MaxSubjectLength:   o.maxSubjectLength,
		MaxBodySize:        o.maxBodySize,
		MaxAttachmentSize:  o.maxAttachmentSize,
		MaxAttachmentCount: o.maxAttachmentCount,
		MaxRecipientCount:  o.maxRecipientCount,
		MaxMetadataSize:    o.maxMetadataSize,
		MaxMetadataKeys:    o.maxMetadataKeys,
	}
}
