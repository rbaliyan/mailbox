package redis

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/notify"
	"github.com/redis/go-redis/v9"
)

// Compile-time interface checks.
var (
	_ notify.Store       = (*Store)(nil)
	_ notify.StreamStore = (*Store)(nil)
)

// Store is a Redis Stream-backed notification store.
//
// Each user gets a dedicated Redis Stream keyed as "{prefix}:{userID}".
// Save appends via XADD, List reads via XRANGE, and Subscribe uses
// XREAD BLOCK for live delivery — collapsing persistence and streaming
// into a single data structure.
//
// The caller manages the Redis client lifecycle.
type Store struct {
	client redis.UniversalClient
	opts   *options
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
}

// New creates a new Redis notification store.
// The caller manages the Redis client lifecycle.
func New(client redis.UniversalClient, opts ...Option) *Store {
	ctx, cancel := context.WithCancel(context.Background())
	return &Store{
		client: client,
		opts:   newOptions(opts...),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (s *Store) key(userID string) string {
	if s.opts.hashTag {
		return fmt.Sprintf("{%s:%s}", s.opts.prefix, userID)
	}
	return fmt.Sprintf("%s:%s", s.opts.prefix, userID)
}

// Save persists a notification event via XADD.
// The stream is automatically trimmed to approximately MaxLen entries.
func (s *Store) Save(ctx context.Context, evt *notify.Event) error {
	if s.closed.Load() {
		return notify.ErrStoreClosed
	}

	id, err := s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.key(evt.UserID),
		MaxLen: s.opts.maxLen,
		Approx: true,
		Values: map[string]any{
			"type":      evt.Type,
			"user_id":   evt.UserID,
			"payload":   string(evt.Payload),
			"timestamp": evt.Timestamp.Format(time.RFC3339Nano),
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("notify: redis xadd: %w", err)
	}
	evt.ID = id
	return nil
}

// SaveBatch persists multiple notification events via pipelined XADD.
// This is significantly faster than individual Save calls for multi-recipient delivery.
func (s *Store) SaveBatch(ctx context.Context, events []*notify.Event) error {
	if s.closed.Load() {
		return notify.ErrStoreClosed
	}
	if len(events) == 0 {
		return nil
	}

	pipe := s.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(events))
	for i, evt := range events {
		cmds[i] = pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: s.key(evt.UserID),
			MaxLen: s.opts.maxLen,
			Approx: true,
			Values: map[string]any{
				"type":      evt.Type,
				"user_id":   evt.UserID,
				"payload":   string(evt.Payload),
				"timestamp": evt.Timestamp.Format(time.RFC3339Nano),
			},
		})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("notify: redis pipeline xadd: %w", err)
	}
	for i, cmd := range cmds {
		if id, err := cmd.Result(); err == nil {
			events[i].ID = id
		}
	}
	return nil
}

// Compile-time check that Store implements BatchSaver.
var _ notify.BatchSaver = (*Store)(nil)

// List returns notifications for a user after the given event ID.
func (s *Store) List(ctx context.Context, userID string, afterID string, limit int) ([]notify.Event, error) {
	if s.closed.Load() {
		return nil, notify.ErrStoreClosed
	}

	if limit <= 0 {
		limit = s.opts.backfillSize
	}

	start := "-"
	if afterID != "" {
		start = "(" + afterID // Exclusive start.
	}

	msgs, err := s.client.XRangeN(ctx, s.key(userID), start, "+", int64(limit)).Result()
	if err != nil {
		return nil, fmt.Errorf("notify: redis xrange: %w", err)
	}

	events := make([]notify.Event, 0, len(msgs))
	for _, msg := range msgs {
		events = append(events, parseMessage(msg, userID))
	}
	return events, nil
}

// Cleanup removes notifications older than the given time.
// Scans all streams matching the prefix and trims entries with MINID.
//
// NOTE: In Redis Cluster, SCAN only visits keys on a single node per call.
// Consider using hash tags in the prefix (e.g., "{notify}") to colocate
// all user streams on the same slot.
func (s *Store) Cleanup(ctx context.Context, olderThan time.Time) error {
	if s.closed.Load() {
		return notify.ErrStoreClosed
	}

	minID := fmt.Sprintf("%d-0", olderThan.UnixMilli())
	pattern := s.opts.prefix + ":*"

	var cursor uint64
	for {
		keys, next, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("notify: redis scan: %w", err)
		}
		for _, key := range keys {
			if err := s.client.XTrimMinID(ctx, key, minID).Err(); err != nil {
				s.opts.logger.Warn("notify: redis xtrim failed",
					"key", key, "error", err)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return nil
}

// Close marks the store as closed and cancels all active streams.
func (s *Store) Close(_ context.Context) error {
	s.closed.Store(true)
	s.cancel()
	return nil
}

// Subscribe returns a Stream backed by Redis XREAD BLOCK.
// If lastEventID is non-empty, missed events are replayed before live delivery.
func (s *Store) Subscribe(ctx context.Context, userID string, lastEventID string) (notify.Stream, error) {
	if s.closed.Load() {
		return nil, notify.ErrStoreClosed
	}

	key := s.key(userID)
	startID := "$" // New events only.

	var backfill []notify.Event

	if lastEventID != "" {
		msgs, err := s.client.XRangeN(ctx, key, "("+lastEventID, "+", int64(s.opts.backfillSize)).Result()
		if err != nil {
			return nil, fmt.Errorf("notify: redis backfill: %w", err)
		}
		backfill = make([]notify.Event, 0, len(msgs))
		for _, msg := range msgs {
			backfill = append(backfill, parseMessage(msg, userID))
		}
		if len(backfill) > 0 {
			startID = backfill[len(backfill)-1].ID
		} else {
			startID = lastEventID
		}
	}

	streamCtx, cancel := context.WithCancel(s.ctx)
	return &stream{
		client:       s.client,
		key:          key,
		lastID:       startID,
		backfill:     backfill,
		ctx:          streamCtx,
		cancel:       cancel,
		blockTimeout: s.opts.blockTimeout,
	}, nil
}

// stream implements notify.Stream using Redis XREAD BLOCK.
type stream struct {
	client       redis.UniversalClient
	key          string
	lastID       string
	backfill     []notify.Event
	backfillIdx  int
	ctx          context.Context
	cancel       context.CancelFunc
	closed       atomic.Bool
	blockTimeout time.Duration
}

var _ notify.Stream = (*stream)(nil)

// Next blocks until the next event is available or ctx is cancelled.
// Backfilled events are drained first, then live events via XREAD BLOCK.
func (s *stream) Next(ctx context.Context) (notify.Event, error) {
	// Drain backfill first.
	if s.backfillIdx < len(s.backfill) {
		evt := s.backfill[s.backfillIdx]
		s.backfillIdx++
		if s.backfillIdx >= len(s.backfill) {
			s.backfill = nil
		}
		return evt, nil
	}

	// Live streaming via XREAD BLOCK.
	for {
		if s.closed.Load() || s.ctx.Err() != nil {
			return notify.Event{}, notify.ErrStreamClosed
		}
		if ctx.Err() != nil {
			return notify.Event{}, ctx.Err()
		}

		readCtx, readCancel := context.WithTimeout(ctx, s.blockTimeout)
		result, err := s.client.XRead(readCtx, &redis.XReadArgs{
			Streams: []string{s.key, s.lastID},
			Count:   1,
			Block:   s.blockTimeout,
		}).Result()
		readCancel()

		if err != nil {
			if errors.Is(err, redis.Nil) || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if s.ctx.Err() != nil {
				return notify.Event{}, notify.ErrStreamClosed
			}
			if ctx.Err() != nil {
				return notify.Event{}, ctx.Err()
			}
			return notify.Event{}, fmt.Errorf("notify: redis xread: %w", err)
		}

		if len(result) == 0 || len(result[0].Messages) == 0 {
			continue
		}

		msg := result[0].Messages[0]
		evt := parseMessage(msg, "")
		s.lastID = evt.ID
		return evt, nil
	}
}

// Close stops the stream. After Close, Next returns ErrStreamClosed.
func (s *stream) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.cancel()
	return nil
}

func parseMessage(msg redis.XMessage, fallbackUserID string) notify.Event {
	evt := notify.Event{ID: msg.ID}

	if v, ok := msg.Values["type"].(string); ok {
		evt.Type = v
	}
	if v, ok := msg.Values["user_id"].(string); ok {
		evt.UserID = v
	} else {
		evt.UserID = fallbackUserID
	}
	if v, ok := msg.Values["payload"].(string); ok {
		evt.Payload = []byte(v)
	}
	if v, ok := msg.Values["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			evt.Timestamp = t
		}
	}
	return evt
}
