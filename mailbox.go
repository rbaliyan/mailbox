package mailbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event/v3/transport/noop"
	eventredis "github.com/rbaliyan/event/v3/transport/redis"
	"github.com/rbaliyan/mailbox/notify"
	"github.com/rbaliyan/mailbox/store"
	"golang.org/x/sync/semaphore"
)

// Compile-time check that service implements Service.
var _ Service = (*service)(nil)

// Connection states for the service.
const (
	stateDisconnected int32 = 0
	stateConnecting   int32 = 1
	stateConnected    int32 = 2
)

// service is the default implementation of Service.
type service struct {
	store             store.Store
	attachments       store.AttachmentManager
	notifier          *notify.Notifier
	logger            *slog.Logger
	cfg               Config
	opts              *options
	state             int32 // stateDisconnected, stateConnecting, or stateConnected
	plugins           *pluginRegistry
	otel              *otelInstrumentation
	sendSem           *semaphore.Weighted // Limits concurrent sends to prevent resource exhaustion
	eventBus          *event.Bus          // Event bus for publishing events
	events            *ServiceEvents      // Per-service event instances
	statsCache        sync.Map            // map[ownerID string]*statsEntry
	statsCacheEnabled bool                // true when event transport is configured
	statsDistCache    StatsCache          // optional distributed L2 cache (e.g., Redis)
	wg                sync.WaitGroup      // Tracks background goroutines
	bgCancel          context.CancelFunc  // Cancels background goroutine context
	mailboxID         string              // Assigned by Registrar during Connect; empty when no registrar
}

// New creates a new mailbox service with the given configuration and options.
// Call Connect() to establish connections to backends and start background tasks.
//
// When Config specifies non-zero cleanup intervals, background goroutines are
// started during Connect() and stopped during Close(). Close() blocks until
// all background goroutines have finished.
//
// Caching is NOT included in this library. If you need caching, wrap your store
// with a caching decorator (see store/cached package for an example).
func New(cfg Config, opts ...Option) (Service, error) {
	cfg.applyDefaults()
	o := newOptions(opts...)

	if o.store == nil {
		return nil, ErrStoreRequired
	}

	// Initialize plugin registry
	plugins := newPluginRegistry(o.logger)
	for _, p := range o.plugins {
		plugins.register(p)
	}

	// Initialize OTel instrumentation
	otelInstr, err := newOtelInstrumentation(o)
	if err != nil {
		return nil, fmt.Errorf("init otel: %w", err)
	}

	return &service{
		store:          o.store,
		attachments:    o.attachments,
		notifier:       o.notifier,
		logger:         o.logger,
		cfg:            cfg,
		opts:           o,
		plugins:        plugins,
		otel:           otelInstr,
		sendSem:        semaphore.NewWeighted(int64(cfg.MaxConcurrentSends)),
		statsDistCache: o.statsDistCache,
	}, nil
}


// Events returns per-service event instances for subscribing and publishing.
func (s *service) Events() *ServiceEvents {
	return s.events
}

// MailboxID returns the mailbox ID assigned by the Registrar during Connect.
// Returns an empty string when no registrar was configured or Connect has not
// completed successfully.
func (s *service) MailboxID() string {
	return s.mailboxID
}

// IsConnected returns true if the service is connected and ready.
func (s *service) IsConnected() bool {
	return atomic.LoadInt32(&s.state) == stateConnected
}

// Connect establishes connections to storage backends.
func (s *service) Connect(ctx context.Context) error {
	// Use three-state to prevent Client() from seeing partial initialization
	// stateDisconnected -> stateConnecting -> stateConnected
	if !atomic.CompareAndSwapInt32(&s.state, stateDisconnected, stateConnecting) {
		if atomic.LoadInt32(&s.state) == stateConnecting {
			return ErrAlreadyConnected // Connection in progress
		}
		return ErrAlreadyConnected
	}

	// Reset to disconnected on failure, set to connected on success
	success := false
	defer func() {
		if success {
			atomic.StoreInt32(&s.state, stateConnected)
		} else {
			atomic.StoreInt32(&s.state, stateDisconnected)
		}
	}()

	if err := s.store.Connect(ctx); err != nil {
		return fmt.Errorf("connect store: %w", err)
	}

	// Initialize event bus with appropriate transport
	if err := s.initEventBus(ctx); err != nil {
		_ = s.store.Close(ctx)
		return fmt.Errorf("init event bus: %w", err)
	}

	// Initialize plugins
	if err := s.plugins.initAll(ctx); err != nil {
		_ = s.eventBus.Close(ctx)
		_ = s.store.Close(ctx)
		return fmt.Errorf("init plugins: %w", err)
	}

	// Register with the configured registrar (if any). The registrar returns
	// the mailbox ID assigned to this instance. A registration failure aborts
	// Connect after rolling back earlier initialization.
	if s.opts.registrar != nil {
		id, err := s.opts.registrar.Register(ctx)
		if err != nil {
			_ = s.plugins.closeAll(ctx)
			_ = s.eventBus.Close(ctx)
			_ = s.store.Close(ctx)
			return fmt.Errorf("register mailbox: %w", err)
		}
		s.mailboxID = id
	}

	// Start background maintenance goroutines
	s.startBackgroundTasks()

	success = true
	s.logger.Info("mailbox service connected", "mailbox_id", s.mailboxID)
	return nil
}

// busCounter generates unique suffixes for event bus names.
var busCounter int64

// initEventBus initializes the event bus for this service.
// Each service creates its own bus. Events are global singletons that get
// bound to the first bus that registers them.
func (s *service) initEventBus(ctx context.Context) error {
	serviceName := s.opts.serviceName
	if serviceName == "" {
		serviceName = "mailbox"
	}
	// Each bus needs a unique name, so append a counter suffix
	busName := fmt.Sprintf("%s-%d", serviceName, atomic.AddInt64(&busCounter, 1))

	var bus *event.Bus
	ownsBus := s.opts.eventBus == nil // true when we create the bus; false when caller provided it

	if !ownsBus {
		// Caller-provided bus: use as-is. Outbox wiring is the caller's responsibility.
		bus = s.opts.eventBus
		s.statsCacheEnabled = true
	} else {
		// Collect bus options.
		var busOpts []event.BusOption

		switch {
		case s.opts.eventTransport != nil:
			s.logger.Info("initializing event bus with custom transport")
			busOpts = append(busOpts, event.WithTransport(s.opts.eventTransport))
			s.statsCacheEnabled = true
		case s.opts.redisClient != nil:
			s.logger.Info("initializing event bus with Redis transport")
			redisOpts := []eventredis.Option{
				eventredis.WithClaimInterval(s.cfg.ClaimInterval, s.cfg.ClaimMinIdle),
				eventredis.WithClaimBatchSize(s.cfg.ClaimBatchSize),
			}
			if s.cfg.EventStreamMaxLen > 0 {
				redisOpts = append(redisOpts, eventredis.WithMaxLen(s.cfg.EventStreamMaxLen))
			}
			t, transportErr := eventredis.New(s.opts.redisClient, redisOpts...)
			if transportErr != nil {
				return fmt.Errorf("create redis transport: %w", transportErr)
			}
			busOpts = append(busOpts, event.WithTransport(t))
			s.statsCacheEnabled = true
		default:
			s.logger.Debug("initializing event bus with noop transport (stats cache disabled)")
			busOpts = append(busOpts, event.WithTransport(noop.New()))
			s.statsCacheEnabled = false
		}

		// Wire outbox store from the store implementation if available.
		if provider, ok := s.store.(store.EventOutboxProvider); ok {
			if outboxStore := provider.EventOutboxStore(); outboxStore != nil {
				busOpts = append(busOpts, event.WithOutbox(outboxStore))
				s.logger.Info("event bus outbox enabled")
			}
		}

		var err error
		bus, err = event.NewBus(busName, busOpts...)
		if err != nil {
			return fmt.Errorf("create event bus: %w", err)
		}
	}

	// closeOnErr tears down the bus on setup failure, but only if we created it.
	// An externally-provided bus is the caller's responsibility to close.
	closeOnErr := func() {
		if ownsBus {
			_ = bus.Close(ctx)
		}
	}

	// Don't assign bus to s.eventBus until all setup succeeds.
	// On failure, closeOnErr() is called and s.eventBus remains nil,
	// preventing later code from seeing a partially-initialized bus.
	events := newServiceEvents(busName)

	// Register per-service events (unique per service instance).
	if err := registerServiceEvents(ctx, bus, events); err != nil {
		closeOnErr()
		return fmt.Errorf("register service events: %w", err)
	}

	// Also register global events for backward compatibility.
	// Global events use "first registration wins" - subsequent calls are no-ops.
	if err := registerEvents(ctx, bus); err != nil {
		closeOnErr()
		return fmt.Errorf("register events: %w", err)
	}

	// Subscribe internal handlers for stats cache updates.
	if err := events.MessageSent.Subscribe(ctx, s.onMessageSent); err != nil {
		closeOnErr()
		return fmt.Errorf("subscribe stats MessageSent: %w", err)
	}
	if err := events.MessageRead.Subscribe(ctx, s.onMessageRead); err != nil {
		closeOnErr()
		return fmt.Errorf("subscribe stats MessageRead: %w", err)
	}
	if err := events.MessageDeleted.Subscribe(ctx, s.onMessageDeleted); err != nil {
		closeOnErr()
		return fmt.Errorf("subscribe stats MessageDeleted: %w", err)
	}
	if err := events.MessageMoved.Subscribe(ctx, s.onMessageMoved); err != nil {
		closeOnErr()
		return fmt.Errorf("subscribe stats MessageMoved: %w", err)
	}

	// Subscribe notification handlers with AsWorker (worker model).
	// Each event is processed by exactly one instance in the consumer group.
	// When notification coalescing is enabled, events with the same message_id
	// in metadata are collapsed so only the latest is delivered.
	if s.notifier != nil {
		coalesce := s.opts.notifyCoalesce
		if err := events.MessageSent.Subscribe(ctx, s.onNotifyMessageSent, notifySubOpts[MessageSentEvent](coalesce)...); err != nil {
			closeOnErr()
			return fmt.Errorf("subscribe notify MessageSent: %w", err)
		}
		if err := events.MessageReceived.Subscribe(ctx, s.onNotifyMessageReceived, notifySubOpts[MessageReceivedEvent](coalesce)...); err != nil {
			closeOnErr()
			return fmt.Errorf("subscribe notify MessageReceived: %w", err)
		}
		if err := events.MessageRead.Subscribe(ctx, s.onNotifyMessageRead, notifySubOpts[MessageReadEvent](coalesce)...); err != nil {
			closeOnErr()
			return fmt.Errorf("subscribe notify MessageRead: %w", err)
		}
		if err := events.MessageDeleted.Subscribe(ctx, s.onNotifyMessageDeleted, notifySubOpts[MessageDeletedEvent](coalesce)...); err != nil {
			closeOnErr()
			return fmt.Errorf("subscribe notify MessageDeleted: %w", err)
		}
		if err := events.MessageMoved.Subscribe(ctx, s.onNotifyMessageMoved, notifySubOpts[MessageMovedEvent](coalesce)...); err != nil {
			closeOnErr()
			return fmt.Errorf("subscribe notify MessageMoved: %w", err)
		}
		if err := events.MarkAllRead.Subscribe(ctx, s.onNotifyMarkAllRead, notifySubOpts[MarkAllReadEvent](coalesce)...); err != nil {
			closeOnErr()
			return fmt.Errorf("subscribe notify MarkAllRead: %w", err)
		}
	}

	// All setup succeeded - commit to service state.
	s.eventBus = bus
	s.events = events
	return nil
}

// notifySubOpts returns subscribe options for notification handlers.
// Always uses AsWorker. Adds WithCoalesceByMetadata when coalescing is enabled.
func notifySubOpts[T any](coalesce bool) []event.SubscribeOption[T] {
	opts := []event.SubscribeOption[T]{event.AsWorker[T]()}
	if coalesce {
		opts = append(opts, event.WithCoalesceByMetadata[T](MetadataMessageID))
	}
	return opts
}

// Close closes connections to storage backends.
// It stops all background maintenance goroutines and waits for them to finish
// before closing the store and event bus.
func (s *service) Close(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.state, stateConnected, stateDisconnected) {
		return nil
	}

	var errs []error

	// Stop background maintenance goroutines.
	if s.bgCancel != nil {
		s.bgCancel()
	}

	// Wait for in-flight send operations to complete (graceful shutdown).
	// After setting state to disconnected, no new sends can start because checkAccess fails.
	// We acquire all semaphore slots to wait for existing operations to finish.
	s.logger.Info("waiting for in-flight operations to complete...", "timeout", s.cfg.ShutdownTimeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, s.cfg.ShutdownTimeout)
	defer shutdownCancel()
	if err := s.sendSem.Acquire(shutdownCtx, int64(s.cfg.MaxConcurrentSends)); err != nil {
		// Context cancelled or deadline exceeded - log but continue shutdown
		s.logger.Warn("timeout waiting for in-flight operations, proceeding with shutdown",
			"error", err)
		errs = append(errs, fmt.Errorf("graceful shutdown timeout: %w", err))
	} else {
		s.sendSem.Release(int64(s.cfg.MaxConcurrentSends))
		s.logger.Info("all in-flight operations completed")
	}

	// Wait for background goroutines to finish after sends are done.
	s.wg.Wait()

	// Close notifier (stops all notification streams).
	if s.notifier != nil {
		if err := s.notifier.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close notifier: %w", err))
		}
	}

	// Close plugins first (reverse order of init)
	if err := s.plugins.closeAll(ctx); err != nil {
		errs = append(errs, fmt.Errorf("close plugins: %w", err))
	}

	// Close event bus only if we created it with a real transport.
	// For noop transport, the bus doesn't hold resources and closing would
	// break events for other services sharing the same global events.
	// For externally-provided buses, the caller is responsible for closing.
	if s.eventBus != nil && s.opts.eventBus == nil && (s.opts.eventTransport != nil || s.opts.redisClient != nil) {
		if err := s.eventBus.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close event bus: %w", err))
		}
	}

	if err := s.store.Close(ctx); err != nil {
		errs = append(errs, fmt.Errorf("close store: %w", err))
	}

	return errors.Join(errs...)
}

// Client returns a mailbox client for the given user.
// Connection state is checked lazily on each operation, not at creation time.
// If the service is not connected, operations on the returned client will
// return ErrNotConnected.
func (s *service) Client(userID string) Mailbox {
	return &userMailbox{
		userID:      userID,
		service:     s,
		validUserID: isValidUserID(userID),
	}
}

// isValidUserID checks if a user ID is valid.
// Valid user IDs are non-empty and contain only safe characters.
// This prevents cache key injection and other security issues.
func isValidUserID(userID string) bool {
	if userID == "" {
		return false
	}
	// Allow alphanumeric, hyphen, underscore, period, at-sign
	// Disallow: *, :, /, \, spaces, and control characters
	for _, c := range userID {
		if c == '*' || c == ':' || c == '/' || c == '\\' ||
			c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
			c < 32 || c == 127 {
			return false
		}
	}
	return true
}

// userMailbox is the default implementation of Mailbox.
type userMailbox struct {
	userID      string
	service     *service
	validUserID bool // set by Client() after validation
}

// UserID returns the user ID of this mailbox.
func (m *userMailbox) UserID() string {
	return m.userID
}

// isConnected checks if the service is connected.
func (m *userMailbox) isConnected() bool {
	return atomic.LoadInt32(&m.service.state) == stateConnected
}

// checkAccess verifies the mailbox is ready for operations.
// Returns ErrNotConnected if service isn't connected,
// or ErrInvalidUserID if user ID failed validation.
func (m *userMailbox) checkAccess() error {
	if !m.isConnected() {
		return ErrNotConnected
	}
	if !m.validUserID {
		return ErrInvalidUserID
	}
	return nil
}
