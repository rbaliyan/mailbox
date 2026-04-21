package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Config holds service-level configuration.
// Zero values use library defaults (see Default* constants in option.go).
// Use DefaultConfig() for a configuration with all defaults,
// preserving backward-compatible behavior.
type Config struct {
	// --- Background Task Scheduling ---

	// TrashCleanupInterval is how often to run automatic trash cleanup.
	// Zero disables automatic trash cleanup (caller must invoke CleanupTrash manually).
	TrashCleanupInterval time.Duration

	// ExpiredMessageCleanupInterval is how often to run automatic expired message cleanup.
	// This covers both global retention (MessageRetention) and per-message TTL expiry.
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

	// --- Trash and Retention ---

	// TrashRetention is how long messages stay in trash before cleanup.
	// Default is 30 days. Minimum is 1 day; shorter values are ignored.
	TrashRetention time.Duration

	// MessageRetention is the global message TTL based on creation time.
	// Messages older than this are permanently deleted by CleanupExpiredMessages.
	// Default is 0 (disabled). Minimum is 1 day when enabled.
	MessageRetention time.Duration

	// --- Message Limits ---

	// MaxSubjectLength is the maximum subject length in characters.
	// Default is 998 (RFC 5322).
	MaxSubjectLength int

	// MaxBodySize is the maximum body size in bytes.
	// Default is 10 MB.
	MaxBodySize int

	// MaxAttachmentSize is the maximum size per attachment in bytes.
	// Default is 25 MB.
	MaxAttachmentSize int64

	// MaxAttachmentCount is the maximum number of attachments per message.
	// Default is 20.
	MaxAttachmentCount int

	// MaxRecipientCount is the maximum number of recipients per message.
	// Default is 100.
	MaxRecipientCount int

	// MaxMetadataSize is the maximum total metadata size in bytes.
	// Default is 64 KB.
	MaxMetadataSize int

	// MaxMetadataKeys is the maximum number of metadata keys per message.
	// Default is 100.
	MaxMetadataKeys int

	// --- Header Limits ---

	// MaxHeaderCount is the maximum number of headers per message.
	// Default is 50.
	MaxHeaderCount int

	// MaxHeaderKeyLength is the maximum length of a single header key.
	// Default is 128 bytes.
	MaxHeaderKeyLength int

	// MaxHeaderValueLength is the maximum length of a single header value.
	// Default is 8 KB.
	MaxHeaderValueLength int

	// MaxHeadersTotalSize is the maximum total size of all headers combined.
	// Default is 64 KB.
	MaxHeadersTotalSize int

	// --- Query Limits ---

	// MaxQueryLimit is the maximum number of messages per query.
	// Any query requesting more than this limit will be capped.
	// Default is 100.
	MaxQueryLimit int

	// DefaultQueryLimit is the default number of messages per query
	// when no limit is specified. Capped to MaxQueryLimit.
	// Default is 20.
	DefaultQueryLimit int

	// --- Concurrency and Shutdown ---

	// MaxConcurrentSends is the maximum number of concurrent send operations.
	// Default is 10.
	MaxConcurrentSends int

	// ShutdownTimeout is the maximum time to wait for in-flight operations
	// during graceful shutdown. Minimum is 1 second.
	// Default is 30 seconds.
	ShutdownTimeout time.Duration

	// --- Per-Message TTL and Scheduling ---

	// DefaultTTL is the default per-message time-to-live. When a message is
	// sent without an explicit TTL, this default is applied.
	// Default is 0 (disabled — messages do not expire unless explicitly set).
	DefaultTTL time.Duration

	// MinTTL is the minimum allowed TTL. Messages with a shorter TTL are rejected.
	// Default is 1 minute.
	MinTTL time.Duration

	// MaxTTL is the maximum allowed TTL. Messages with a longer TTL are rejected.
	// Default is 0 (unlimited).
	MaxTTL time.Duration

	// MinScheduleDelay is the minimum allowed schedule delay from now.
	// Default is 0 (no minimum).
	MinScheduleDelay time.Duration

	// MaxScheduleDelay is the maximum allowed schedule delay from now.
	// Default is 0 (unlimited).
	MaxScheduleDelay time.Duration

	// --- Stats Cache ---

	// StatsRefreshInterval is the TTL for cached mailbox stats.
	// Default is 30 seconds.
	StatsRefreshInterval time.Duration

	// --- Redis Event Transport Tuning ---

	// ClaimInterval is how often the background goroutine scans for orphaned
	// messages in Redis consumer groups. Default is 30 seconds.
	ClaimInterval time.Duration

	// ClaimMinIdle is the minimum idle time before a message is eligible
	// for claiming from dead consumers. Default is 60 seconds.
	ClaimMinIdle time.Duration

	// ClaimBatchSize is the maximum number of orphaned messages to claim per cycle.
	// Default is 100.
	ClaimBatchSize int64

	// EventStreamMaxLen is the approximate maximum number of entries per
	// Redis event stream. Older entries are trimmed automatically on each publish.
	// Default is 10,000 (see DefaultEventStreamMaxLen).
	// Set to -1 to disable trimming (not recommended for production — streams grow without bound).
	EventStreamMaxLen int64
}

// QuotaUserLister provides the list of user IDs for background quota enforcement.
// Implementations should be safe for concurrent use.
type QuotaUserLister interface {
	// ListUsers returns user IDs that should be checked for quota enforcement.
	ListUsers(ctx context.Context) ([]string, error)
}

// DefaultConfig returns a Config with all fields set to their library defaults.
// Background maintenance tasks are disabled (intervals are zero); enable them
// by setting the relevant interval fields.
//
// Use DefaultConfig as a starting point and override individual fields:
//
//	cfg := mailbox.DefaultConfig()
//	cfg.TrashCleanupInterval = 1 * time.Hour
//	cfg.EventStreamMaxLen = 50_000
func DefaultConfig() Config {
	cfg := Config{}
	cfg.applyDefaults()
	return cfg
}

// Validate checks that all critical resource limits are explicitly bounded.
// Call this after constructing a Config to catch missing limits before Connect.
//
// Returns a joined error listing every field that is zero or unlimited.
// Fields that are intentionally unbounded (MessageRetention, MaxTTL, MaxScheduleDelay)
// are not checked — they have documented "0 = disabled/unlimited" semantics.
func (c *Config) Validate() error {
	var errs []error

	check := func(name string, val int64) {
		if val <= 0 {
			errs = append(errs, fmt.Errorf("%s must be > 0 (got %d)", name, val))
		}
	}

	check("MaxSubjectLength", int64(c.MaxSubjectLength))
	check("MaxBodySize", int64(c.MaxBodySize))
	check("MaxAttachmentSize", c.MaxAttachmentSize)
	check("MaxAttachmentCount", int64(c.MaxAttachmentCount))
	check("MaxRecipientCount", int64(c.MaxRecipientCount))
	check("MaxMetadataSize", int64(c.MaxMetadataSize))
	check("MaxMetadataKeys", int64(c.MaxMetadataKeys))
	check("MaxHeaderCount", int64(c.MaxHeaderCount))
	check("MaxHeaderKeyLength", int64(c.MaxHeaderKeyLength))
	check("MaxHeaderValueLength", int64(c.MaxHeaderValueLength))
	check("MaxHeadersTotalSize", int64(c.MaxHeadersTotalSize))
	check("MaxQueryLimit", int64(c.MaxQueryLimit))
	check("DefaultQueryLimit", int64(c.DefaultQueryLimit))
	check("MaxConcurrentSends", int64(c.MaxConcurrentSends))
	check("ShutdownTimeout", int64(c.ShutdownTimeout))
	check("TrashRetention", int64(c.TrashRetention))
	check("MinTTL", int64(c.MinTTL))
	check("StatsRefreshInterval", int64(c.StatsRefreshInterval))
	check("ClaimInterval", int64(c.ClaimInterval))
	check("ClaimMinIdle", int64(c.ClaimMinIdle))
	check("ClaimBatchSize", c.ClaimBatchSize)

	// EventStreamMaxLen must be positive; -1 (unlimited) is a known production risk.
	if c.EventStreamMaxLen <= 0 {
		errs = append(errs, fmt.Errorf(
			"EventStreamMaxLen must be > 0 (got %d); unlimited streams grow without bound in Redis",
			c.EventStreamMaxLen,
		))
	}

	return errors.Join(errs...)
}

// applyDefaults fills zero-valued fields with library defaults and validates constraints.
func (c *Config) applyDefaults() {
	// Trash and retention
	if c.TrashRetention == 0 {
		c.TrashRetention = DefaultTrashRetention
	} else if c.TrashRetention < MinTrashRetention {
		c.TrashRetention = DefaultTrashRetention
	}
	// MessageRetention: 0 = disabled (valid); < MinMessageRetention when non-zero = invalid → reset to 0
	if c.MessageRetention != 0 && c.MessageRetention < MinMessageRetention {
		c.MessageRetention = 0
	}

	// Message limits
	if c.MaxSubjectLength <= 0 {
		c.MaxSubjectLength = DefaultMaxSubjectLength
	}
	if c.MaxBodySize <= 0 {
		c.MaxBodySize = DefaultMaxBodySize
	}
	if c.MaxAttachmentSize <= 0 {
		c.MaxAttachmentSize = DefaultMaxAttachmentSize
	}
	if c.MaxAttachmentCount <= 0 {
		c.MaxAttachmentCount = DefaultMaxAttachmentCount
	}
	if c.MaxRecipientCount <= 0 {
		c.MaxRecipientCount = DefaultMaxRecipientCount
	}
	if c.MaxMetadataSize <= 0 {
		c.MaxMetadataSize = DefaultMaxMetadataSize
	}
	if c.MaxMetadataKeys <= 0 {
		c.MaxMetadataKeys = DefaultMaxMetadataKeys
	}

	// Header limits
	if c.MaxHeaderCount <= 0 {
		c.MaxHeaderCount = DefaultMaxHeaderCount
	}
	if c.MaxHeaderKeyLength <= 0 {
		c.MaxHeaderKeyLength = DefaultMaxHeaderKeyLength
	}
	if c.MaxHeaderValueLength <= 0 {
		c.MaxHeaderValueLength = DefaultMaxHeaderValueLength
	}
	if c.MaxHeadersTotalSize <= 0 {
		c.MaxHeadersTotalSize = DefaultMaxHeadersTotalSize
	}

	// Query limits
	if c.MaxQueryLimit <= 0 {
		c.MaxQueryLimit = DefaultMaxQueryLimit
	}
	if c.DefaultQueryLimit <= 0 {
		c.DefaultQueryLimit = DefaultQueryLimit
	}
	if c.DefaultQueryLimit > c.MaxQueryLimit {
		c.DefaultQueryLimit = c.MaxQueryLimit
	}

	// Concurrency and shutdown
	if c.MaxConcurrentSends <= 0 {
		c.MaxConcurrentSends = DefaultMaxConcurrentSends
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	} else if c.ShutdownTimeout < MinShutdownTimeout {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}

	// Per-message TTL and scheduling
	// DefaultTTL: 0 = disabled (valid)
	// MinTTL: 0 → use default (1 minute)
	if c.MinTTL <= 0 {
		c.MinTTL = 1 * time.Minute
	}
	// MaxTTL: 0 = unlimited (valid)
	// MinScheduleDelay: 0 = no minimum (valid)
	// MaxScheduleDelay: 0 = unlimited (valid)

	// Stats cache
	if c.StatsRefreshInterval <= 0 {
		c.StatsRefreshInterval = DefaultStatsRefreshInterval
	}

	// Redis event transport tuning
	if c.ClaimInterval <= 0 {
		c.ClaimInterval = DefaultClaimInterval
	}
	if c.ClaimMinIdle <= 0 {
		c.ClaimMinIdle = DefaultClaimMinIdle
	}
	if c.ClaimBatchSize <= 0 {
		c.ClaimBatchSize = DefaultClaimBatchSize
	}
	// EventStreamMaxLen: 0 = use default; -1 = unlimited (streams grow without bound)
	if c.EventStreamMaxLen == 0 {
		c.EventStreamMaxLen = DefaultEventStreamMaxLen
	}
}

// getLimits returns the configured message limits.
func (c *Config) getLimits() MessageLimits {
	return MessageLimits{
		MaxSubjectLength:     c.MaxSubjectLength,
		MaxBodySize:          c.MaxBodySize,
		MaxAttachmentSize:    c.MaxAttachmentSize,
		MaxAttachmentCount:   c.MaxAttachmentCount,
		MaxRecipientCount:    c.MaxRecipientCount,
		MaxMetadataSize:      c.MaxMetadataSize,
		MaxMetadataKeys:      c.MaxMetadataKeys,
		MaxHeaderCount:       c.MaxHeaderCount,
		MaxHeaderKeyLength:   c.MaxHeaderKeyLength,
		MaxHeaderValueLength: c.MaxHeaderValueLength,
		MaxHeadersTotalSize:  c.MaxHeadersTotalSize,
	}
}
