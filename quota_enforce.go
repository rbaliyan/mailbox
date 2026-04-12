package mailbox

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// EnforceQuotasResult contains the result of a quota enforcement run.
type EnforceQuotasResult struct {
	// UsersChecked is the number of users whose quotas were evaluated.
	UsersChecked int
	// UsersOverQuota is the number of users found over their quota limit.
	UsersOverQuota int
	// MessagesDeleted is the total number of messages deleted across all users.
	MessagesDeleted int
	// Interrupted indicates if the enforcement was stopped early (e.g., context cancelled).
	Interrupted bool
}

// EnforceQuotas evaluates quotas for the given users and applies enforcement actions.
// Only users with QuotaActionDeleteOldest policies are processed; users with
// QuotaActionReject are skipped since rejection happens at delivery time.
//
// For each over-quota user, the method deletes the oldest messages that are older
// than the policy's DeleteOlderThan threshold, up to the amount needed to bring
// the user back within their quota limit.
//
// When Config.QuotaEnforcementInterval and Config.QuotaUserLister are set, this is
// called automatically by a background goroutine. It can also be called manually
// with an explicit user list for on-demand enforcement.
func (s *service) EnforceQuotas(ctx context.Context, userIDs []string) (*EnforceQuotasResult, error) {
	if atomic.LoadInt32(&s.state) != stateConnected {
		return nil, ErrNotConnected
	}

	result := &EnforceQuotasResult{}

	if s.opts.quotaProvider == nil {
		return result, nil
	}

	for _, userID := range userIDs {
		if ctx.Err() != nil {
			result.Interrupted = true
			return result, ctx.Err()
		}

		deleted, err := s.enforceUserQuota(ctx, userID)
		result.UsersChecked++
		if err != nil {
			s.logger.Warn("quota enforcement failed for user",
				"user_id", userID, "error", err)
			continue
		}
		if deleted > 0 {
			result.UsersOverQuota++
			result.MessagesDeleted += deleted
		}
	}

	return result, nil
}

// enforceUserQuota checks and enforces the quota for a single user.
// Returns the number of messages deleted (0 if user is within quota or uses reject mode).
func (s *service) enforceUserQuota(ctx context.Context, userID string) (int, error) {
	policy, err := s.opts.quotaProvider.GetQuota(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("get quota: %w", err)
	}
	if policy == nil || policy.MaxMessages <= 0 {
		return 0, nil
	}
	if policy.ExceedAction != QuotaActionDeleteOldest {
		return 0, nil
	}
	if policy.DeleteOlderThan <= 0 {
		s.logger.Warn("skipping quota enforcement: DeleteOlderThan not configured",
			"user_id", userID, "action", policy.ExceedAction)
		return 0, nil
	}

	// Get current message count.
	count, err := s.store.Count(ctx, []store.Filter{store.OwnerIs(userID)})
	if err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}

	excess := count - policy.MaxMessages
	if excess <= 0 {
		return 0, nil
	}

	// Find the oldest messages that are older than the age threshold.
	cutoff := time.Now().UTC().Add(-policy.DeleteOlderThan)
	createdBeforeFilter, err := store.MessageFilter("CreatedAt").LessThan(cutoff)
	if err != nil {
		return 0, fmt.Errorf("create age filter: %w", err)
	}

	filters := []store.Filter{
		store.OwnerIs(userID),
		createdBeforeFilter,
	}

	// Fetch candidates sorted by creation time ascending (oldest first).
	// Limit to the excess count to avoid fetching more than needed.
	limit := int(excess)
	list, err := s.store.Find(ctx, filters, store.ListOptions{
		Limit:     limit,
		SortBy:    "created_at",
		SortOrder: store.SortAsc,
	})
	if err != nil {
		return 0, fmt.Errorf("find old messages: %w", err)
	}

	deleted := 0
	for _, msg := range list.Messages {
		if ctx.Err() != nil {
			return deleted, ctx.Err()
		}

		// Release attachment references before deleting.
		if s.attachments != nil {
			for _, att := range msg.GetAttachments() {
				if err := s.attachments.RemoveRef(ctx, att.GetID()); err != nil {
					s.logger.Warn("failed to release attachment ref during quota enforcement",
						"error", err, "attachment_id", att.GetID(), "message_id", msg.GetID())
				}
			}
		}

		if err := s.store.HardDelete(ctx, msg.GetID()); err != nil {
			s.logger.Warn("failed to delete message during quota enforcement",
				"error", err, "message_id", msg.GetID(), "user_id", userID)
			continue
		}
		deleted++
	}

	if deleted > 0 {
		s.logger.Debug("enforced quota for user",
			"user_id", userID, "deleted", deleted, "excess", excess)
	}

	return deleted, nil
}
