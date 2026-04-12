package mailbox

import (
	"context"
	"time"
)

// Config holds service-level configuration for background task scheduling.
// Zero-value intervals disable the corresponding background task.
// Use DefaultConfig() for a configuration with all tasks disabled,
// preserving backward-compatible behavior.
type Config struct {
	// TrashCleanupInterval is how often to run automatic trash cleanup.
	// Zero disables automatic trash cleanup (caller must invoke CleanupTrash manually).
	TrashCleanupInterval time.Duration

	// ExpiredMessageCleanupInterval is how often to run automatic expired message cleanup.
	// This covers both global retention (WithMessageRetention) and per-message TTL expiry.
	// Zero disables automatic cleanup (caller must invoke CleanupExpiredMessages manually).
	ExpiredMessageCleanupInterval time.Duration

	// QuotaEnforcementInterval is how often to run automatic quota enforcement.
	// Requires QuotaUserLister to be set; ignored if nil.
	// Zero disables automatic quota enforcement (caller must invoke EnforceQuotas manually).
	QuotaEnforcementInterval time.Duration

	// QuotaUserLister provides the list of user IDs for background quota enforcement.
	// Required when QuotaEnforcementInterval > 0. If nil, quota enforcement is skipped
	// even when the interval is set.
	QuotaUserLister QuotaUserLister
}

// QuotaUserLister provides the list of user IDs for background quota enforcement.
// Implementations should be safe for concurrent use.
type QuotaUserLister interface {
	// ListUsers returns user IDs that should be checked for quota enforcement.
	ListUsers(ctx context.Context) ([]string, error)
}

// DefaultConfig returns a Config with all background tasks disabled.
// This is used by NewService to preserve backward-compatible behavior
// where the caller is responsible for scheduling maintenance tasks.
func DefaultConfig() Config {
	return Config{}
}
