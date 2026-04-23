package mailbox

import (
	"context"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// StatsCache is an optional distributed cache for mailbox stats.
// When configured via WithStatsCache, it acts as an L2 cache behind the
// in-process sync.Map (L1), enabling consistent stats across instances.
// Implementations must be safe for concurrent use.
type StatsCache interface {
	// Get retrieves cached stats for a user. Returns nil, nil when not found.
	Get(ctx context.Context, ownerID string) (*store.MailboxStats, error)

	// Set stores stats for a user with the given TTL.
	Set(ctx context.Context, ownerID string, stats *store.MailboxStats, ttl time.Duration) error

	// IncrBy atomically increments a named counter for a user.
	// field is one of the well-known keys defined in the statscache package.
	IncrBy(ctx context.Context, ownerID string, field string, delta int64) error

	// Invalidate removes cached stats for a user, forcing the next read
	// to fall through to the store.
	Invalidate(ctx context.Context, ownerID string) error
}

// Known field names for StatsCache.IncrBy. Using constants prevents typos and
// makes it easy to change the encoding in one place.
const (
	StatsCacheFieldTotal  = "total"
	StatsCacheFieldUnread = "unread"
	StatsCacheFieldDraft  = "draft"
)

// StatsCacheFolderTotal returns the IncrBy field name for a folder's total count.
func StatsCacheFolderTotal(folderID string) string { return "folder:" + folderID + ":total" }

// StatsCacheFolderUnread returns the IncrBy field name for a folder's unread count.
func StatsCacheFolderUnread(folderID string) string { return "folder:" + folderID + ":unread" }
