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
	"github.com/rbaliyan/mailbox/store"
	"golang.org/x/sync/semaphore"
)

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
	logger            *slog.Logger
	opts              *options
	state             int32 // stateDisconnected, stateConnecting, or stateConnected
	plugins           *pluginRegistry
	otel              *otelInstrumentation
	sendSem           *semaphore.Weighted // Limits concurrent sends to prevent resource exhaustion
	eventBus          *event.Bus          // Event bus for publishing events
	events            *ServiceEvents      // Per-service event instances
	statsCache        sync.Map            // map[ownerID string]*statsEntry
	statsCacheEnabled bool                // true when event transport is configured
}

// NewService creates a new mailbox service.
// Call Connect() to establish connections to backends.
//
// Caching is NOT included in this library. If you need caching, wrap your store
// with a caching decorator (see store/cached package for an example).
// This keeps the library focused on messaging while letting you control caching strategy.
func NewService(opts ...Option) (Service, error) {
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
		store:       o.store,
		attachments: o.attachments,
		logger:      o.logger,
		opts:        o,
		plugins:     plugins,
		otel:        otelInstr,
		sendSem:     semaphore.NewWeighted(int64(o.maxConcurrentSends)),
	}, nil
}

// Events returns per-service event instances for subscribing and publishing.
func (s *service) Events() *ServiceEvents {
	return s.events
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

	success = true
	s.logger.Info("mailbox service connected")
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
	var err error

	switch {
	case s.opts.eventTransport != nil:
		s.logger.Info("initializing event bus with custom transport")
		bus, err = event.NewBus(busName, event.WithTransport(s.opts.eventTransport))
		s.statsCacheEnabled = true
	case s.opts.redisClient != nil:
		s.logger.Info("initializing event bus with Redis transport")
		t, transportErr := eventredis.New(s.opts.redisClient)
		if transportErr != nil {
			return fmt.Errorf("create redis transport: %w", transportErr)
		}
		bus, err = event.NewBus(busName, event.WithTransport(t))
		s.statsCacheEnabled = true
	default:
		s.logger.Debug("initializing event bus with noop transport (stats cache disabled)")
		bus, err = event.NewBus(busName, event.WithTransport(noop.New()))
		s.statsCacheEnabled = false
	}

	if err != nil {
		return fmt.Errorf("create event bus: %w", err)
	}

	// Don't assign bus to s.eventBus until all setup succeeds.
	// On failure, bus.Close() is called and s.eventBus remains nil,
	// preventing later code from seeing a partially-initialized closed bus.
	events := newServiceEvents(busName)

	// Register per-service events (unique per service instance).
	if err := registerServiceEvents(ctx, bus, events); err != nil {
		_ = bus.Close(ctx)
		return fmt.Errorf("register service events: %w", err)
	}

	// Also register global events for backward compatibility.
	// Global events use "first registration wins" - subsequent calls are no-ops.
	if err := registerEvents(ctx, bus); err != nil {
		_ = bus.Close(ctx)
		return fmt.Errorf("register events: %w", err)
	}

	// Subscribe internal handlers for stats cache updates.
	if err := events.MessageSent.Subscribe(ctx, s.onMessageSent); err != nil {
		_ = bus.Close(ctx)
		return fmt.Errorf("subscribe stats MessageSent: %w", err)
	}
	if err := events.MessageRead.Subscribe(ctx, s.onMessageRead); err != nil {
		_ = bus.Close(ctx)
		return fmt.Errorf("subscribe stats MessageRead: %w", err)
	}
	if err := events.MessageDeleted.Subscribe(ctx, s.onMessageDeleted); err != nil {
		_ = bus.Close(ctx)
		return fmt.Errorf("subscribe stats MessageDeleted: %w", err)
	}
	if err := events.MessageMoved.Subscribe(ctx, s.onMessageMoved); err != nil {
		_ = bus.Close(ctx)
		return fmt.Errorf("subscribe stats MessageMoved: %w", err)
	}

	// All setup succeeded - commit to service state.
	s.eventBus = bus
	s.events = events
	return nil
}

// Close closes connections to storage backends.
func (s *service) Close(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&s.state, stateConnected, stateDisconnected) {
		return nil
	}

	var errs []error

	// Wait for in-flight send operations to complete (graceful shutdown).
	// After setting state to disconnected, no new sends can start because checkAccess fails.
	// We acquire all semaphore slots to wait for existing operations to finish.
	s.logger.Info("waiting for in-flight operations to complete...", "timeout", s.opts.shutdownTimeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, s.opts.shutdownTimeout)
	defer shutdownCancel()
	if err := s.sendSem.Acquire(shutdownCtx, int64(s.opts.maxConcurrentSends)); err != nil {
		// Context cancelled or deadline exceeded - log but continue shutdown
		s.logger.Warn("timeout waiting for in-flight operations, proceeding with shutdown",
			"error", err)
		errs = append(errs, fmt.Errorf("graceful shutdown timeout: %w", err))
	} else {
		s.sendSem.Release(int64(s.opts.maxConcurrentSends))
		s.logger.Info("all in-flight operations completed")
	}

	// Close plugins first (reverse order of init)
	if err := s.plugins.closeAll(ctx); err != nil {
		errs = append(errs, fmt.Errorf("close plugins: %w", err))
	}

	// Close event bus only if using a real transport.
	// For noop transport, the bus doesn't hold resources and closing would
	// break events for other services sharing the same global events.
	if s.eventBus != nil && (s.opts.eventTransport != nil || s.opts.redisClient != nil) {
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
