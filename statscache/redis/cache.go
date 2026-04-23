package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"github.com/redis/go-redis/v9"
)

// Local field names — mirror the exported StatsCacheField* / StatsCacheFolder*
// constants in the root mailbox package. Duplicated here to avoid a circular
// import; keep both in sync when adding new counter fields.
const (
	fieldTotal  = "total"
	fieldUnread = "unread"
	fieldDraft  = "draft"
)

func folderTotal(id string) string  { return "folder:" + id + ":total" }
func folderUnread(id string) string { return "folder:" + id + ":unread" }

// Cache is a Redis Hash-backed distributed stats cache.
//
// Each user's stats are stored as a Redis Hash at key "{prefix}:{userID}".
// Hash fields: "total", "unread", "draft", and per-folder
// "folder:{folderID}:total" / "folder:{folderID}:unread" entries.
//
// HINCRBY keeps individual counters consistent without a read-modify-write cycle.
// HGETALL deserializes the full stats on a cache miss.
type Cache struct {
	client redis.UniversalClient
	opts   *options
}

// New creates a new Redis stats cache.
func New(client redis.UniversalClient, opts ...Option) *Cache {
	o := &options{
		prefix: defaultPrefix,
		ttl:    defaultTTL,
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return &Cache{client: client, opts: o}
}

// Get retrieves cached stats for a user. Returns nil, nil when not cached.
func (c *Cache) Get(ctx context.Context, ownerID string) (*store.MailboxStats, error) {
	key := c.key(ownerID)
	fields, err := c.client.HGetAll(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("stats cache get: %w", err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	stats, err := decodeStats(fields)
	if err != nil {
		c.opts.logger.Warn("stats cache decode error, invalidating", "owner", ownerID, "error", err)
		_ = c.client.Del(ctx, key)
		return nil, nil
	}
	return stats, nil
}

// Set stores stats for a user with the given TTL.
func (c *Cache) Set(ctx context.Context, ownerID string, stats *store.MailboxStats, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = c.opts.ttl
	}
	key := c.key(ownerID)
	fields := encodeStats(stats)

	pipe := c.client.Pipeline()
	pipe.HSet(ctx, key, fields)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("stats cache set: %w", err)
	}
	return nil
}

// incrByScript atomically checks key existence and increments a hash field.
// If the key does not exist the script is a no-op, preventing partial entries.
// Using a script eliminates the TOCTOU window between EXISTS and HINCRBY.
var incrByScript = redis.NewScript(`
local exists = redis.call('EXISTS', KEYS[1])
if exists == 0 then return 0 end
redis.call('HINCRBY', KEYS[1], ARGV[1], ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`)

// IncrBy atomically increments a named field for a user.
// If the key does not exist (cache miss), the increment is skipped rather than
// seeding partial data — the next Get will miss and trigger a full store fetch.
func (c *Cache) IncrBy(ctx context.Context, ownerID string, field string, delta int64) error {
	if delta == 0 {
		return nil
	}
	ttlMs := c.opts.ttl.Milliseconds()
	err := incrByScript.Run(ctx, c.client,
		[]string{c.key(ownerID)},
		field, delta, ttlMs,
	).Err()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("stats cache incrby: %w", err)
	}
	return nil
}

// Invalidate removes cached stats for a user.
func (c *Cache) Invalidate(ctx context.Context, ownerID string) error {
	if err := c.client.Del(ctx, c.key(ownerID)).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("stats cache invalidate: %w", err)
	}
	return nil
}

// key returns the Redis key for a user's stats hash.
func (c *Cache) key(ownerID string) string {
	return c.opts.prefix + ":" + ownerID
}

// encodeStats serializes MailboxStats into a flat map for HSET.
// Folders are written as individual "folder:<id>:total" and
// "folder:<id>:unread" fields so that HINCRBY can update them in place.
func encodeStats(s *store.MailboxStats) map[string]any {
	m := map[string]any{
		fieldTotal:  s.TotalMessages,
		fieldUnread: s.UnreadCount,
		fieldDraft:  s.DraftCount,
	}
	for folderID, counts := range s.Folders {
		m[folderTotal(folderID)] = counts.Total
		m[folderUnread(folderID)] = counts.Unread
	}
	return m
}

// decodeStats deserializes a Redis Hash into MailboxStats.
// Folder counts are reconstructed from the per-field "folder:<id>:total" and
// "folder:<id>:unread" entries so Get always reflects the latest HINCRBY state.
// Returns an error if any numeric field cannot be parsed so callers can invalidate
// the corrupted cache entry rather than serving zeroed data.
func decodeStats(fields map[string]string) (*store.MailboxStats, error) {
	s := &store.MailboxStats{
		Folders: make(map[string]store.FolderCounts),
	}

	for k, v := range fields {
		if !strings.HasPrefix(k, "folder:") {
			continue
		}
		parts := strings.Split(k, ":")
		if len(parts) != 3 {
			continue
		}
		folderID := parts[1]
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse field %q: %w", k, err)
		}
		counts := s.Folders[folderID]
		switch parts[2] {
		case "total":
			counts.Total = n
		case "unread":
			counts.Unread = n
		}
		s.Folders[folderID] = counts
	}

	if v, ok := fields[fieldTotal]; ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse field %q: %w", fieldTotal, err)
		}
		s.TotalMessages = n
	}
	if v, ok := fields[fieldUnread]; ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse field %q: %w", fieldUnread, err)
		}
		s.UnreadCount = n
	}
	if v, ok := fields[fieldDraft]; ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse field %q: %w", fieldDraft, err)
		}
		s.DraftCount = n
	}

	return s, nil
}
