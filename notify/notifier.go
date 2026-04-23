package notify

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"
)

// Notifier manages per-user notification delivery and persistence.
// It coordinates between the event bus, presence tracking, notification
// store, routing, and local SSE streams.
//
// The typical flow:
//  1. Service event handler calls Push (runs on the AsWorker instance).
//  2. Push checks presence — if user is offline, the event is dropped.
//  3. Push saves to Store (for backfill on reconnect).
//  4. Push delivers to local streams (if user is connected to this instance).
//  5. If user is on another instance and a Router is configured, route there.
//  6. Otherwise, the remote instance discovers events via store polling.
type Notifier struct {
	opts    *options
	streams sync.Map // map[string]*userStreams — local per-user stream sets
	closed  atomic.Bool

	// OTel instruments (nil when no meter provider configured)
	activeCount  atomic.Int64
	streamsGauge metric.Int64Gauge // mailbox.notify.streams.active
}

// userStreams holds the set of active streams for a single user on this instance.
type userStreams struct {
	mu      sync.Mutex
	streams []*stream
}

// NewNotifier creates a new Notifier with the given options.
func NewNotifier(opts ...Option) *Notifier {
	n := &Notifier{
		opts: newOptions(opts...),
	}
	if n.opts.meter != nil {
		meter := n.opts.meter.Meter("github.com/rbaliyan/mailbox")
		var err error
		n.streamsGauge, err = meter.Int64Gauge(
			"mailbox.notify.streams.active",
			metric.WithDescription("Number of active notification subscription streams on this instance"),
		)
		if err != nil {
			n.opts.logger.Warn("notify: failed to create streams.active gauge", "error", err)
		}
	}
	return n
}

// Push sends a notification to a user.
//
// When presence tracking is configured and the user is offline, Push returns
// immediately without saving or routing — the event is dropped. When presence
// is not configured, or when the user is online, the event is persisted to the
// store (for backfill on reconnect) and then delivered either to a local stream
// (user connected to this instance) or forwarded via the Router (user connected
// to another instance).
func (n *Notifier) Push(ctx context.Context, userID string, evt Event) error {
	if n.closed.Load() {
		return ErrNotifierClosed
	}

	evt.UserID = userID

	// Check presence if configured.
	if n.opts.presence != nil {
		online, err := n.opts.presence.IsOnline(ctx, userID)
		if err != nil {
			n.opts.logger.Warn("notify: presence check failed, saving anyway",
				"user_id", userID, "error", err)
		} else if !online {
			return nil // User offline — skip.
		}
	}

	// Persist for backfill.
	if n.opts.store != nil {
		if err := n.opts.store.Save(ctx, &evt); err != nil {
			return err
		}
	}

	// If store supports native streaming (e.g., Redis Streams), Save is
	// the delivery mechanism — subscribers pick up events via XREAD BLOCK.
	if _, ok := n.opts.store.(StreamStore); ok {
		return nil
	}

	// Try local delivery first.
	if n.deliverLocal(userID, evt) {
		return nil
	}

	// User not connected locally — try routing to the remote instance.
	n.tryRoute(ctx, userID, evt)

	return nil
}

// PushMulti sends a notification to multiple users efficiently.
// When the store implements BatchSaver, all events are saved in a single pipeline.
// Otherwise, falls back to individual Push calls.
func (n *Notifier) PushMulti(ctx context.Context, userIDs []string, evt Event) (failed int, err error) {
	if n.closed.Load() {
		return 0, ErrNotifierClosed
	}

	// Filter online users if presence is configured.
	targets := userIDs
	if n.opts.presence != nil {
		online, err := n.opts.presence.OnlineUsers(ctx, userIDs)
		if err != nil {
			n.opts.logger.Warn("notify: presence check failed, pushing to all",
				"error", err)
		} else {
			targets = online
		}
	}

	if len(targets) == 0 {
		return 0, nil
	}

	// Persist for backfill — batch if supported.
	if n.opts.store != nil {
		events := make([]*Event, len(targets))
		for i, uid := range targets {
			e := evt // copy
			e.UserID = uid
			events[i] = &e
		}

		if bs, ok := n.opts.store.(BatchSaver); ok {
			if err := bs.SaveBatch(ctx, events); err != nil {
				return len(targets), err
			}
		} else {
			for _, e := range events {
				if err := n.opts.store.Save(ctx, e); err != nil {
					failed++
					n.opts.logger.Warn("notify: save failed", "user_id", e.UserID, "error", err)
				}
			}
			if failed == len(targets) {
				return failed, fmt.Errorf("notify: all %d saves failed", failed)
			}
		}
	}

	// If store supports native streaming, Save is the delivery mechanism.
	if _, ok := n.opts.store.(StreamStore); ok {
		return failed, nil
	}

	// Local delivery + routing for non-streaming stores.
	for _, uid := range targets {
		e := evt
		e.UserID = uid
		if !n.deliverLocal(uid, e) {
			n.tryRoute(ctx, uid, e)
		}
	}

	return failed, nil
}

// Deliver pushes an event directly to a local stream, bypassing presence
// checks and store persistence. This is used by Router implementations
// on the receiving side of cross-instance delivery.
func (n *Notifier) Deliver(userID string, evt Event) {
	n.deliverLocal(userID, evt)
}

// Subscribe opens a notification stream for the user.
// If lastEventID is non-empty, the stream replays events after that ID
// from the store before switching to live delivery.
// The returned Stream must be closed by the caller.
//
// Subscribe does NOT register presence — the caller should manage presence
// registration separately (e.g., at the SSE handler level) since presence
// is an independent module.
func (n *Notifier) Subscribe(ctx context.Context, userID string, lastEventID string) (Stream, error) {
	if n.closed.Load() {
		return nil, ErrNotifierClosed
	}

	// Use native streaming if the store supports it (e.g., Redis Streams).
	if ss, ok := n.opts.store.(StreamStore); ok {
		return ss.Subscribe(ctx, userID, lastEventID)
	}

	streamCtx, cancel := context.WithCancel(context.Background())
	s := &stream{
		ch:           make(chan Event, n.opts.bufferSize),
		store:        n.opts.store,
		userID:       userID,
		lastID:       lastEventID,
		pollInterval: n.opts.pollInterval,
		ctx:          streamCtx,
		cancel:       cancel,
	}

	// Backfill from store.
	if n.opts.store != nil && lastEventID != "" {
		events, err := n.opts.store.List(ctx, userID, lastEventID, 0)
		if err != nil {
			cancel()
			return nil, err
		}
		for _, evt := range events {
			select {
			case s.ch <- evt:
				s.lastID = evt.ID
			default:
				// Buffer full — consumer will pick up via polling.
			}
		}
	}

	// Register in local stream set before starting the goroutine so that
	// notifier.Close() is guaranteed to see the stream if it runs concurrently.
	n.addStream(userID, s)

	// Re-check closed after addStream to close the window where Close() finished
	// its Range (saw no stream) before addStream inserted it. In that case we
	// self-cancel and remove rather than starting an ungoverned goroutine.
	if n.closed.Load() {
		n.removeStream(userID, s)
		s.cancel()
		return nil, ErrNotifierClosed
	}

	// Start background store poller for events from other instances.
	if n.opts.store != nil {
		s.wg.Go(s.pollLoop)
	}

	return s, nil
}

// Close shuts down the notifier and all active streams.
// It cancels all stream contexts and waits for every pollLoop goroutine to exit.
func (n *Notifier) Close(_ context.Context) error {
	if !n.closed.CompareAndSwap(false, true) {
		return nil
	}

	// Cancel all stream contexts. The channel is never closed — context
	// cancellation is the sole termination signal, avoiding send-on-closed races.
	var toWait []*stream
	n.streams.Range(func(key, value any) bool {
		us := value.(*userStreams)
		us.mu.Lock()
		for _, s := range us.streams {
			s.cancel()
			s.closed.Store(true)
			toWait = append(toWait, s)
		}
		us.streams = nil
		us.mu.Unlock()
		n.streams.Delete(key)
		return true
	})

	// Wait for all pollLoop goroutines to exit before returning.
	// Each goroutine holds s.ctx (now cancelled) and will exit on the next tick.
	for _, s := range toWait {
		s.wg.Wait()
	}

	return nil
}

// deliverLocal pushes an event to all local streams for the user.
// Returns true if at least one local stream received the event.
func (n *Notifier) deliverLocal(userID string, evt Event) bool {
	val, ok := n.streams.Load(userID)
	if !ok {
		return false
	}
	us := val.(*userStreams)
	us.mu.Lock()
	defer us.mu.Unlock()

	delivered := false
	for _, s := range us.streams {
		if s.closed.Load() {
			continue
		}
		select {
		case s.ch <- evt:
			delivered = true
		default:
			s.dropped.Add(1)
		}
	}
	return delivered
}

// tryRoute attempts to route an event to the instance where the user is connected.
// Requires both a Router and Presence (with routing info) to be configured.
// Failures are logged and silently ignored — the remote instance will
// pick up the event via store polling.
func (n *Notifier) tryRoute(ctx context.Context, userID string, evt Event) {
	if n.opts.router == nil || n.opts.presence == nil {
		return
	}

	info, err := n.opts.presence.Locate(ctx, userID)
	if err != nil {
		return // User not found or error — store polling will handle it.
	}

	// Don't route to ourselves.
	if n.opts.instanceID != "" && info.InstanceID == n.opts.instanceID {
		return
	}

	routingInfo := RoutingInfo{
		InstanceID: info.InstanceID,
		Metadata:   info.Metadata,
	}

	if err := n.opts.router.Route(ctx, routingInfo, evt); err != nil {
		n.opts.logger.Warn("notify: route failed, falling back to store polling",
			"user_id", userID,
			"instance_id", info.InstanceID,
			"error", err,
		)
	}
}

func (n *Notifier) addStream(userID string, s *stream) {
	val, _ := n.streams.LoadOrStore(userID, &userStreams{})
	us := val.(*userStreams)
	us.mu.Lock()
	us.streams = append(us.streams, s)
	s.notifier = n
	us.mu.Unlock()
	count := n.activeCount.Add(1)
	if n.streamsGauge != nil {
		n.streamsGauge.Record(context.Background(), count)
	}
}

func (n *Notifier) removeStream(userID string, s *stream) {
	val, ok := n.streams.Load(userID)
	if !ok {
		return
	}
	us := val.(*userStreams)
	us.mu.Lock()
	defer us.mu.Unlock()
	for i, existing := range us.streams {
		if existing == s {
			us.streams = append(us.streams[:i], us.streams[i+1:]...)
			break
		}
	}
	if len(us.streams) == 0 {
		n.streams.Delete(userID)
	}
	count := n.activeCount.Add(-1)
	if n.streamsGauge != nil {
		n.streamsGauge.Record(context.Background(), count)
	}
}
