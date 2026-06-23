package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	event "github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event/v3/transport"
	"github.com/rbaliyan/mailbox/store"
)

// errSubscribeUnsupported is returned by the capturing transport's Subscribe:
// the relay path only publishes, so subscription is never needed here.
var errSubscribeUnsupported = errors.New("storetest: capturing transport does not support Subscribe")

// relayTestEvent is the payload published through the outbox during the relay
// suite. It is intentionally tiny and self-contained so the suite does not
// depend on the mailbox event types.
type relayTestEvent struct {
	MessageID string `json:"message_id"`
	OwnerID   string `json:"owner_id"`
}

// RelayRunner drives a single outbox relay pass. Backends supply a concrete
// relay (event/v3/outbox.Relay for PostgreSQL, event-mongodb/outbox.MongoRelay
// for MongoDB) whose PublishOnce method satisfies this interface, letting the
// shared suite trigger one drain of the outbox without time.Sleep.
type RelayRunner interface {
	// PublishOnce processes one batch of pending outbox rows, publishing each to
	// the transport and marking it published. It returns after a single pass.
	PublishOnce(ctx context.Context) error
}

// RelayFactory builds a backend-specific RelayRunner that reads the SAME outbox
// the store writes to and publishes to the supplied transport. The transport
// passed here is the capturing transport owned by the suite, so the suite can
// observe exactly what the relay published.
type RelayFactory func(t *testing.T, s store.Store, tr transport.Transport) RelayRunner

// RunRelaySuite verifies the end-to-end transactional-outbox relay path against
// a real backend:
//
//  1. An event bus is built over a capturing transport and the store's
//     event.OutboxStore (via WithOutbox), exactly as the mailbox service wires
//     it in production.
//  2. An event is published inside store.WithOutboxCtx. Because the context
//     carries the outbox transaction marker, the publish is routed to the
//     outbox table/collection rather than the transport — so nothing reaches
//     the capturing transport yet.
//  3. The real relay (built by relayFactory) is run with PublishOnce against
//     the same transport. The suite polls the capturing transport until the
//     event surfaces, then asserts it was delivered exactly once.
//
// The suite skips unless the store has the outbox enabled. Pass a factory that
// builds the store with the outbox on (e.g. WithOutbox(true)).
func RunRelaySuite(t *testing.T, newStore NewStoreFunc, relayFactory RelayFactory) {
	s := newStore(t)

	op, ok := s.(store.OutboxPersister)
	if !ok || !op.OutboxEnabled() {
		t.Skip("store does not have the outbox enabled")
	}
	provider, ok := s.(store.EventOutboxProvider)
	if !ok || provider.EventOutboxStore() == nil {
		t.Skip("store does not expose an event.OutboxStore")
	}

	ctx := ctxT(t)

	// Capturing transport records everything the relay publishes.
	cap := newCapturingTransport()
	t.Cleanup(func() { _ = cap.Close(context.Background()) })

	// Build a bus wired like the mailbox service: capturing transport + outbox.
	// A process-unique bus name avoids collisions with other suites/backends.
	busName := uniqueOwner("relay-bus")
	bus, err := event.NewBus(busName,
		event.WithTransport(cap),
		event.WithOutbox(provider.EventOutboxStore()),
	)
	if err != nil {
		t.Fatalf("NewBus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close(context.Background()) })

	evt := event.New[relayTestEvent](busName + ".relay.test.event")
	if err := event.Register(ctx, bus, evt); err != nil {
		t.Fatalf("Register event: %v", err)
	}

	owner := uniqueOwner("relay-owner")

	// Write a message AND publish the event inside one outbox transaction.
	var created store.Message
	err = op.WithOutboxCtx(ctx, func(txCtx context.Context) error {
		m, cerr := s.CreateMessage(txCtx, sentData(owner, "relayed", "body"))
		if cerr != nil {
			return cerr
		}
		created = m
		// Routed to the outbox (not the transport) because txCtx carries the
		// outbox transaction marker.
		return evt.Publish(txCtx, relayTestEvent{MessageID: m.GetID(), OwnerID: owner})
	})
	if err != nil {
		// A failure here while routing Event.Publish() into the outbox would
		// indicate the store's outbox storage schema is incompatible with the
		// event.OutboxStore returned by EventOutboxStore() — i.e. the two halves
		// of the store's own outbox wiring disagree on the table/collection
		// shape. That is a real backend defect, so it is asserted strictly rather
		// than masked: the transactional-outbox publish path must succeed for an
		// outbox-enabled store.
		t.Fatalf("writing an event into the outbox via EventOutboxStore() failed (the store's "+
			"outbox-table schema created in Connect must match the schema the event.OutboxStore "+
			"reads/writes): %v", err)
	}

	// The committed mutation must be visible.
	if _, err := s.Get(ctx, created.GetID()); err != nil {
		t.Fatalf("committed message not visible: %v", err)
	}

	// Before the relay runs, the transport must NOT have seen anything: the
	// publish was captured by the outbox, proving routing worked. The relay
	// publishes under whatever event name the outbox row stored (which the bus
	// may namespace), so the suite counts total publishes rather than relying on
	// the exact transport-side name.
	if n := cap.total(); n != 0 {
		t.Fatalf("an event reached the transport before the relay ran (count=%d); "+
			"the publish was not routed to the outbox", n)
	}

	// Run the real relay until the event surfaces on the transport. PublishOnce
	// drains one batch; we poll (no sleep) in case the first pass races the
	// outbox write becoming visible.
	relay := relayFactory(t, s, cap)
	eventually(t, 20*time.Second, 50*time.Millisecond, func() bool {
		if perr := relay.PublishOnce(ctx); perr != nil {
			t.Fatalf("relay PublishOnce: %v", perr)
		}
		return cap.total() >= 1
	}, "relay did not publish the outbox event to the transport")

	// Exactly-once: drive a few more relay passes and confirm the count does not
	// climb. A correct relay marks the row published after the first pass and
	// never republishes it.
	for i := 0; i < 3; i++ {
		if perr := relay.PublishOnce(ctx); perr != nil {
			t.Fatalf("relay PublishOnce (idempotency pass %d): %v", i, perr)
		}
	}
	if n := cap.total(); n != 1 {
		t.Fatalf("relay published the event %d times, want exactly once", n)
	}
}

// capturingTransport is a minimal transport.Transport that records every
// published message per event name. It implements just enough of the interface
// for the bus + relay to operate: RegisterEvent/Publish/Close. Subscribe is not
// exercised by the relay path and returns an error if called.
type capturingTransport struct {
	mu         sync.Mutex
	registered map[string]bool
	published  map[string][]transport.Message
}

func newCapturingTransport() *capturingTransport {
	return &capturingTransport{
		registered: make(map[string]bool),
		published:  make(map[string][]transport.Message),
	}
}

func (c *capturingTransport) RegisterEvent(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registered[name] = true
	return nil
}

func (c *capturingTransport) UnregisterEvent(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.registered, name)
	return nil
}

func (c *capturingTransport) Publish(_ context.Context, name string, msg transport.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.published[name] = append(c.published[name], msg)
	return nil
}

func (c *capturingTransport) Subscribe(_ context.Context, _ string, _ ...transport.SubscribeOption) (transport.Subscription, error) {
	return nil, errSubscribeUnsupported
}

func (c *capturingTransport) Close(_ context.Context) error { return nil }

// total returns the number of messages published across all event names.
func (c *capturingTransport) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int
	for _, msgs := range c.published {
		n += len(msgs)
	}
	return n
}
