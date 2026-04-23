// Package memory provides an in-memory presence.Tracker for testing.
// Registrations are visible only within the current process and are lost
// on restart. For production multi-instance deployments use presence/redis.
package memory

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/rbaliyan/mailbox/presence"
)

// Compile-time interface check.
var _ presence.Tracker = (*Tracker)(nil)

// Tracker is an in-memory presence tracker for single-instance and testing.
type Tracker struct {
	mu      sync.RWMutex
	users   map[string]*userEntry
	closed  int32
}

type userEntry struct {
	count   int32
	routing *presence.RoutingInfo
}

// New creates a new in-memory presence tracker.
func New() *Tracker {
	return &Tracker{
		users: make(map[string]*userEntry),
	}
}

// Register marks a user as online.
func (t *Tracker) Register(_ context.Context, userID string, opts ...presence.RegisterOption) (presence.Registration, error) {
	if atomic.LoadInt32(&t.closed) != 0 {
		return nil, presence.ErrTrackerClosed
	}

	regOpts := presence.ApplyRegisterOptions(opts...)

	t.mu.Lock()
	entry, ok := t.users[userID]
	if !ok {
		entry = &userEntry{}
		t.users[userID] = entry
	}
	entry.count++
	if regOpts.Routing != nil {
		entry.routing = regOpts.Routing
	}
	t.mu.Unlock()

	return &registration{tracker: t, userID: userID}, nil
}

// IsOnline returns true if the user has at least one active registration.
func (t *Tracker) IsOnline(_ context.Context, userID string) (bool, error) {
	if atomic.LoadInt32(&t.closed) != 0 {
		return false, presence.ErrTrackerClosed
	}

	t.mu.RLock()
	entry := t.users[userID]
	t.mu.RUnlock()
	return entry != nil && entry.count > 0, nil
}

// OnlineUsers filters the given user IDs and returns only those currently online.
func (t *Tracker) OnlineUsers(_ context.Context, userIDs []string) ([]string, error) {
	if atomic.LoadInt32(&t.closed) != 0 {
		return nil, presence.ErrTrackerClosed
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	online := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		if entry := t.users[id]; entry != nil && entry.count > 0 {
			online = append(online, id)
		}
	}
	return online, nil
}

// Locate returns routing information for an online user.
func (t *Tracker) Locate(_ context.Context, userID string) (*presence.RoutingInfo, error) {
	if atomic.LoadInt32(&t.closed) != 0 {
		return nil, presence.ErrTrackerClosed
	}

	t.mu.RLock()
	entry := t.users[userID]
	t.mu.RUnlock()

	if entry == nil || entry.count == 0 {
		return nil, presence.ErrNotFound
	}
	if entry.routing == nil {
		return &presence.RoutingInfo{}, nil
	}
	return entry.routing, nil
}

// Close releases resources.
func (t *Tracker) Close(_ context.Context) error {
	atomic.StoreInt32(&t.closed, 1)
	return nil
}

func (t *Tracker) unregister(userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry := t.users[userID]; entry != nil {
		entry.count--
		if entry.count <= 0 {
			delete(t.users, userID)
		}
	}
}

type registration struct {
	tracker      *Tracker
	userID       string
	unregistered int32
}

var _ presence.Registration = (*registration)(nil)

func (r *registration) Unregister(_ context.Context) error {
	if !atomic.CompareAndSwapInt32(&r.unregistered, 0, 1) {
		return nil
	}
	r.tracker.unregister(r.userID)
	return nil
}
