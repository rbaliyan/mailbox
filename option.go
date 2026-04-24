package mailbox

import (
	"log/slog"
	"time"

	"github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event/v3/transport"
	"github.com/rbaliyan/mailbox/notify"
	"github.com/rbaliyan/mailbox/router"
	"github.com/rbaliyan/mailbox/store"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Default configuration values.
const (
	DefaultTrashRetention    = 30 * 24 * time.Hour // 30 days
	MinTrashRetention        = 24 * time.Hour      // 1 day minimum
	DefaultMessageRetention  time.Duration = 0       // disabled by default
	MinMessageRetention                   = 24 * time.Hour // 1 day minimum when enabled
	DefaultShutdownTimeout = 30 * time.Second    // default graceful shutdown timeout
	MinShutdownTimeout     = 1 * time.Second     // minimum shutdown timeout

	// Default message limits
	DefaultMaxSubjectLength   = 998              // RFC 5322 max line length
	DefaultMaxBodySize        = 10 * 1024 * 1024 // 10 MB
	DefaultMaxAttachmentSize  = 25 * 1024 * 1024 // 25 MB per attachment
	DefaultMaxAttachmentCount = 20               // max attachments per message
	DefaultMaxRecipientCount  = 100              // max recipients per message
	DefaultMaxMetadataSize    = 64 * 1024        // 64 KB total metadata
	DefaultMaxMetadataKeys    = 100              // max metadata keys

	// Default header limits
	DefaultMaxHeaderCount       = 50              // max headers per message
	DefaultMaxHeaderKeyLength   = 128             // max header key length
	DefaultMaxHeaderValueLength = 8 * 1024        // 8 KB max header value
	DefaultMaxHeadersTotalSize  = 64 * 1024       // 64 KB total headers

	// Query limits
	DefaultMaxQueryLimit = 100 // max messages per query
	DefaultQueryLimit    = 20  // default messages per query

	// Concurrency limits
	DefaultMaxConcurrentSends = 10 // max concurrent send operations per service

	// Stats cache
	DefaultStatsRefreshInterval = 30 * time.Second // TTL for cached stats

	// Orphan message claiming defaults (Redis transport only)
	DefaultClaimInterval  = 30 * time.Second // scan for orphans every 30s
	DefaultClaimMinIdle   = 60 * time.Second // claim messages idle for 60s+
	DefaultClaimBatchSize = int64(100)        // claim up to 100 messages per cycle

	// DefaultEventStreamMaxLen is the default approximate maximum number of entries
	// per Redis event stream. Set EventStreamMaxLen = -1 to disable trimming.
	DefaultEventStreamMaxLen = int64(10_000)
)

// options holds mailbox configuration.
type options struct {
	store       store.Store
	attachments store.AttachmentManager
	logger      *slog.Logger

	plugins []Plugin

	// OpenTelemetry
	tracingEnabled bool
	metricsEnabled bool
	serviceName    string
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider

	// Notifications
	notifier *notify.Notifier // Per-user notification system (optional)

	// Distributed stats cache (optional L2 behind the in-process sync.Map)
	statsDistCache StatsCache

	// Quota
	quotaProvider QuotaProvider // Optional quota provider for per-user message limits

	// Event handling
	eventErrorsFatal      bool                    // If true, event publishing failures cause operation to fail
	eventBus              *event.Bus              // Pre-created event bus (optional; caller retains ownership)
	eventTransport        transport.Transport     // Event transport (optional, uses noop if nil)
	redisClient           redis.UniversalClient   // Redis client for event transport (optional, uses noop if nil)
	onEventPublishFailure EventPublishFailureFunc // Callback for event publish failures (always set)

	// Notification coalescing
	notifyCoalesce bool // If true, coalesce notification events by message ID

	// User resolution
	userResolver UserResolver // Optional resolver for sender identity metadata

	// Mailbox registration (for multi-instance deployments)
	registrar router.Registrar // Optional registrar called during Connect
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

// newOptions creates options with defaults and applies functional options.
func newOptions(opts ...Option) *options {
	o := &options{
		logger: slog.Default(),
	}

	// Apply functional options.
	for _, opt := range opts {
		opt(o)
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

// --- Mailbox Registration Options ---

// WithRegistrar sets a registrar that the service uses during Connect to
// announce this mailbox instance to a shared registry. The registrar
// returns the mailbox ID assigned to this instance; if Register returns
// an error, Connect fails. The assigned ID is available via Service.MailboxID().
func WithRegistrar(r router.Registrar) Option {
	return func(o *options) {
		if r != nil {
			o.registrar = r
		}
	}
}

// --- User Resolution Options ---

// WithUserResolver sets an optional user resolver for sender identity enrichment.
// When configured, the service resolves the sender's identity during message
// delivery and populates metadata keys (sender.firstname, sender.lastname, sender.email).
// If resolution fails, the send operation is aborted with ErrUserResolveFailed.
func WithUserResolver(r UserResolver) Option {
	return func(o *options) {
		if r != nil {
			o.userResolver = r
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
//	svc, _ := mailbox.New(mailbox.Config{}, mailbox.WithEventTransport(transport))
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

// WithEventBus sets a pre-created event bus for the service.
// The caller retains ownership of the bus — the service will NOT close it.
// This is useful in tests where multiple services share a single bus, or when
// the bus must be configured with specific options (outbox, transport, etc.)
// before the service is created.
//
// WithEventBus takes priority over WithEventTransport and WithRedisClient.
// Outbox wiring is skipped because the caller configured the bus.
//
// Example:
//
//	bus, _ := event.NewBus("test", event.WithTransport(channel.New()))
//	defer bus.Close(ctx)
//
//	svc, _ := mailbox.New(cfg, mailbox.WithStore(store), mailbox.WithEventBus(bus))
func WithEventBus(bus *event.Bus) Option {
	return func(o *options) {
		if bus != nil {
			o.eventBus = bus
		}
	}
}

// --- Notification Options ---

// WithStatsCache sets a distributed cache for aggregate mailbox stats.
// When set, stats reads check this cache (L2) before the primary store,
// and event-driven incremental updates propagate to it via HINCRBY.
// This reduces database load during cold starts and across many instances.
func WithStatsCache(c StatsCache) Option {
	return func(o *options) {
		if c != nil {
			o.statsDistCache = c
		}
	}
}

// WithNotifier sets the per-user notification system.
// When provided, the service subscribes to mailbox events using AsWorker
// (worker model) and pushes notifications to connected users.
// Use svc.Notifications(ctx, userID, lastEventID) to open a notification stream.
func WithNotifier(n *notify.Notifier) Option {
	return func(o *options) {
		if n != nil {
			o.notifier = n
		}
	}
}

// --- Quota Options ---

// WithQuotaProvider sets a custom quota provider for per-user message limits.
// The provider is consulted during message delivery to check recipient quotas.
// When nil (default), quotas are disabled and delivery is unrestricted.
func WithQuotaProvider(p QuotaProvider) Option {
	return func(o *options) {
		if p != nil {
			o.quotaProvider = p
		}
	}
}

// WithGlobalQuota sets a uniform quota policy for all users.
// This is a convenience wrapper around WithQuotaProvider using a StaticQuotaProvider.
func WithGlobalQuota(policy QuotaPolicy) Option {
	return func(o *options) {
		o.quotaProvider = &StaticQuotaProvider{Policy: policy}
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

// WithNotificationCoalescing enables event coalescing for notification handlers.
// When enabled, multiple events for the same message ID within the coalescing
// window are collapsed — only the latest event is delivered to the notification
// stream. This reduces SSE noise for rapidly-updated messages (e.g., a message
// that is sent, then immediately moved or read).
//
// Requires message_id metadata on published events (automatically added).
// Default is disabled.
func WithNotificationCoalescing(enabled bool) Option {
	return func(o *options) {
		o.notifyCoalesce = enabled
	}
}

