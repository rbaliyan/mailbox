package mailbox

import (
	"context"
	"fmt"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// QuotaAction defines what happens when a user exceeds their quota.
type QuotaAction int

const (
	// QuotaActionReject rejects new incoming messages for over-quota users.
	// Delivery to the over-quota recipient fails with ErrQuotaExceeded,
	// while other recipients receive the message normally (partial delivery).
	//
	// Note: The check is best-effort due to the "no distributed locks" principle.
	// Concurrent deliveries may both pass the check before either creates a message,
	// resulting in a small overshoot. The background EnforceQuotas method handles drift.
	QuotaActionReject QuotaAction = iota

	// QuotaActionDeleteOldest accepts all messages and trims old ones in the background.
	// Messages older than QuotaPolicy.DeleteOlderThan are deleted when EnforceQuotas is called.
	// Delivery is never blocked; enforcement happens via the caller-scheduled EnforceQuotas method.
	QuotaActionDeleteOldest
)

// String returns a human-readable name for the quota action.
func (a QuotaAction) String() string {
	switch a {
	case QuotaActionReject:
		return "reject"
	case QuotaActionDeleteOldest:
		return "delete_oldest"
	default:
		return fmt.Sprintf("QuotaAction(%d)", int(a))
	}
}

// QuotaPolicy defines the quota limits and enforcement behavior for a user.
type QuotaPolicy struct {
	// MaxMessages is the maximum number of messages allowed in a user's mailbox.
	// Set to 0 for unlimited (no quota enforcement).
	MaxMessages int64

	// ExceedAction determines what happens when the quota is exceeded.
	// Default is QuotaActionReject.
	ExceedAction QuotaAction

	// DeleteOlderThan is the minimum age of messages eligible for deletion
	// when ExceedAction is QuotaActionDeleteOldest. Messages younger than
	// this duration are never deleted by quota enforcement.
	// Must be positive when ExceedAction is QuotaActionDeleteOldest;
	// enforcement is skipped if zero to prevent accidental deletion of recent messages.
	DeleteOlderThan time.Duration
}

// QuotaProvider resolves quota policies for users.
// Implementations can look up per-user quotas from any source (database, config, etc.).
type QuotaProvider interface {
	// GetQuota returns the quota policy for the given user.
	// Return a nil policy or a policy with MaxMessages=0 for unlimited quota.
	GetQuota(ctx context.Context, userID string) (*QuotaPolicy, error)
}

// Compile-time check that StaticQuotaProvider implements QuotaProvider.
var _ QuotaProvider = (*StaticQuotaProvider)(nil)

// StaticQuotaProvider returns the same quota policy for all users.
// Use this for global quotas that apply uniformly.
type StaticQuotaProvider struct {
	Policy QuotaPolicy
}

// GetQuota returns the static policy for any user.
func (p *StaticQuotaProvider) GetQuota(_ context.Context, _ string) (*QuotaPolicy, error) {
	return &p.Policy, nil
}

// QuotaExceededError provides details about a quota violation.
type QuotaExceededError struct {
	UserID       string
	CurrentCount int64
	MaxMessages  int64
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("mailbox: quota exceeded for user %s (%d/%d messages)",
		e.UserID, e.CurrentCount, e.MaxMessages)
}

func (e *QuotaExceededError) Unwrap() error {
	return ErrQuotaExceeded
}

// checkQuotaWithPolicy checks whether the given user is within the provided quota policy.
// Returns nil if the policy is nil, unlimited, or the user is under quota.
// Returns a QuotaExceededError if the user has reached or exceeded their limit.
func (s *service) checkQuotaWithPolicy(ctx context.Context, userID string, policy *QuotaPolicy) error {
	if policy == nil || policy.MaxMessages <= 0 {
		return nil
	}

	// Use the stats cache for fast count lookup (event-driven, no extra DB query).
	// Falls back to store.Count when the stats cache is disabled (no event transport).
	var count int64
	var err error
	if s.statsCacheEnabled {
		stats, statsErr := s.getOrRefreshStats(ctx, userID)
		if statsErr != nil {
			return fmt.Errorf("get stats for quota check: %w", statsErr)
		}
		count = stats.TotalMessages
	} else {
		count, err = s.store.Count(ctx, []store.Filter{store.OwnerIs(userID)})
		if err != nil {
			return fmt.Errorf("count messages for quota check: %w", err)
		}
	}

	if count >= policy.MaxMessages {
		return &QuotaExceededError{
			UserID:       userID,
			CurrentCount: count,
			MaxMessages:  policy.MaxMessages,
		}
	}

	return nil
}
