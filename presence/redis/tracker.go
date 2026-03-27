package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/presence"
	"github.com/redis/go-redis/v9"
)

// Compile-time interface check.
var _ presence.Tracker = (*Tracker)(nil)

// Tracker is a Redis-backed presence tracker for multi-instance deployments.
//
// Each user's presence is stored as a Redis hash with two fields:
//   - "count": number of active registrations (supports multiple tabs/devices)
//   - "routing": JSON-encoded routing information (optional)
//
// The hash has a TTL that is periodically refreshed by active registrations.
type Tracker struct {
	client redis.UniversalClient
	opts   *options
	closed int32

	mu            sync.Mutex
	registrations []*registration // Active registrations for cleanup on Close
}

// New creates a new Redis presence tracker.
// The caller manages the Redis client lifecycle.
func New(client redis.UniversalClient, opts ...Option) *Tracker {
	return &Tracker{
		client: client,
		opts:   newOptions(opts...),
	}
}

func (t *Tracker) key(userID string) string {
	return fmt.Sprintf("%s:%s", t.opts.prefix, userID)
}

// Register marks a user as online.
// When routing info is provided via WithRouting, it is stored in the hash.
func (t *Tracker) Register(ctx context.Context, userID string, opts ...presence.RegisterOption) (presence.Registration, error) {
	if atomic.LoadInt32(&t.closed) != 0 {
		return nil, presence.ErrTrackerClosed
	}

	regOpts := presence.ApplyRegisterOptions(opts...)
	key := t.key(userID)

	// Increment counter, optionally set routing, and set TTL atomically.
	routingJSON := ""
	if regOpts.Routing != nil {
		data, err := json.Marshal(regOpts.Routing)
		if err != nil {
			return nil, fmt.Errorf("presence: marshal routing: %w", err)
		}
		routingJSON = string(data)
	}

	script := redis.NewScript(`
		local count = redis.call('HINCRBY', KEYS[1], 'count', 1)
		if ARGV[2] ~= '' then
			redis.call('HSET', KEYS[1], 'routing', ARGV[2])
		end
		redis.call('EXPIRE', KEYS[1], ARGV[1])
		return count
	`)
	if err := script.Run(ctx, t.client, []string{key}, int(t.opts.ttl.Seconds()), routingJSON).Err(); err != nil {
		return nil, fmt.Errorf("presence: register %s: %w", userID, err)
	}

	regCtx, cancel := context.WithCancel(context.Background())
	reg := &registration{
		tracker: t,
		userID:  userID,
		cancel:  cancel,
	}

	// Track for cleanup on Close.
	t.mu.Lock()
	t.registrations = append(t.registrations, reg)
	t.mu.Unlock()

	go reg.refreshLoop(regCtx)

	return reg, nil
}

// IsOnline returns true if the user has at least one active registration.
func (t *Tracker) IsOnline(ctx context.Context, userID string) (bool, error) {
	if atomic.LoadInt32(&t.closed) != 0 {
		return false, presence.ErrTrackerClosed
	}

	n, err := t.client.Exists(ctx, t.key(userID)).Result()
	if err != nil {
		return false, fmt.Errorf("presence: check %s: %w", userID, err)
	}
	return n > 0, nil
}

// OnlineUsers filters the given user IDs and returns only those currently online.
func (t *Tracker) OnlineUsers(ctx context.Context, userIDs []string) ([]string, error) {
	if atomic.LoadInt32(&t.closed) != 0 {
		return nil, presence.ErrTrackerClosed
	}
	if len(userIDs) == 0 {
		return nil, nil
	}

	pipe := t.client.Pipeline()
	cmds := make([]*redis.IntCmd, len(userIDs))
	for i, id := range userIDs {
		cmds[i] = pipe.Exists(ctx, t.key(id))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("presence: check online users: %w", err)
	}

	online := make([]string, 0, len(userIDs))
	for i, cmd := range cmds {
		if cmd.Val() > 0 {
			online = append(online, userIDs[i])
		}
	}
	return online, nil
}

// Locate returns routing information for an online user.
// Returns ErrNotFound if the user has no active presence.
func (t *Tracker) Locate(ctx context.Context, userID string) (*presence.RoutingInfo, error) {
	if atomic.LoadInt32(&t.closed) != 0 {
		return nil, presence.ErrTrackerClosed
	}

	result, err := t.client.HGetAll(ctx, t.key(userID)).Result()
	if err != nil {
		return nil, fmt.Errorf("presence: locate %s: %w", userID, err)
	}
	if len(result) == 0 {
		return nil, presence.ErrNotFound
	}

	info := &presence.RoutingInfo{}
	if routingJSON, ok := result["routing"]; ok && routingJSON != "" {
		if err := json.Unmarshal([]byte(routingJSON), info); err != nil {
			return nil, fmt.Errorf("presence: unmarshal routing for %s: %w", userID, err)
		}
	}
	return info, nil
}

// Close stops all active refresh loops and marks the tracker as closed.
// Active registrations' goroutines are cancelled to prevent leaks.
func (t *Tracker) Close(_ context.Context) error {
	if !atomic.CompareAndSwapInt32(&t.closed, 0, 1) {
		return nil
	}

	t.mu.Lock()
	regs := t.registrations
	t.registrations = nil
	t.mu.Unlock()

	for _, reg := range regs {
		reg.cancel()
	}
	return nil
}

func (t *Tracker) removeRegistration(reg *registration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, r := range t.registrations {
		if r == reg {
			t.registrations = append(t.registrations[:i], t.registrations[i+1:]...)
			return
		}
	}
}

type registration struct {
	tracker      *Tracker
	userID       string
	cancel       context.CancelFunc
	unregistered int32
}

var _ presence.Registration = (*registration)(nil)

// Unregister decrements the presence counter and stops the refresh loop.
func (r *registration) Unregister(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&r.unregistered, 0, 1) {
		return nil
	}
	r.cancel()
	r.tracker.removeRegistration(r)

	key := r.tracker.key(r.userID)

	// Decrement counter; clean up hash if it reaches zero.
	script := redis.NewScript(`
		local count = redis.call('HINCRBY', KEYS[1], 'count', -1)
		if count <= 0 then
			redis.call('DEL', KEYS[1])
			return 0
		end
		return count
	`)
	if err := script.Run(ctx, r.tracker.client, []string{key}).Err(); err != nil {
		return fmt.Errorf("presence: unregister %s: %w", r.userID, err)
	}
	return nil
}

// refreshLoop periodically refreshes the TTL on the presence key.
func (r *registration) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(r.tracker.opts.refreshAt)
	defer ticker.Stop()

	key := r.tracker.key(r.userID)
	ttl := r.tracker.opts.ttl

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.tracker.client.Expire(context.Background(), key, ttl).Err(); err != nil {
				r.tracker.opts.logger.Warn("presence: failed to refresh TTL",
					"user_id", r.userID,
					"error", err,
				)
			}
		}
	}
}
