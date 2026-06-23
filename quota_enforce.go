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

// RunQuotaEnforcement lists all users via the configured QuotaUserLister and calls
// EnforceQuotas for them. Returns ErrQuotaUserListerNotConfigured when no lister is set.
// This is the preferred entry point for on-demand admin-triggered enforcement because
// it does not accept user IDs from external callers, eliminating the taint path from
// user-provided input to the database queries inside enforceUserQuota.
func (s *service) RunQuotaEnforcement(ctx context.Context) (*EnforceQuotasResult, error) {
	if s.cfg.QuotaUserLister == nil {
		return nil, ErrQuotaUserListerNotConfigured
	}
	userIDs, err := s.cfg.QuotaUserLister.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return s.EnforceQuotas(ctx, userIDs)
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
			s.otel.recordQuotaEnforced(ctx, userID, deleted)
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

	// quotaDeleteBatchSize caps how many candidates are fetched per page so a
	// large excess does not load an unbounded result set into memory.
	const quotaDeleteBatchSize = 500

	deleted := 0
	// Loop with pagination until the user is back within quota or no more
	// eligible (old enough) messages remain. A single Find pass can under-delete
	// when the page is smaller than the excess, so we keep deleting in batches.
	for {
		if ctx.Err() != nil {
			return deleted, ctx.Err()
		}

		// remaining is how many more messages must be deleted to reach quota,
		// accounting for what this pass has already removed.
		remaining := excess - int64(deleted)
		if remaining <= 0 {
			break
		}

		// Bound the page size and guard against int overflow on 32-bit platforms
		// when excess exceeds the int range.
		limit := quotaDeleteBatchSize
		if remaining < int64(limit) {
			limit = int(remaining)
		}

		// Always fetch oldest-first; deleted messages drop out of the result set
		// so the next page surfaces the next-oldest eligible candidates.
		list, err := s.store.Find(ctx, filters, store.ListOptions{
			Limit:     limit,
			SortBy:    "created_at",
			SortOrder: store.SortAsc,
		})
		if err != nil {
			return deleted, fmt.Errorf("find old messages: %w", err)
		}
		if len(list.Messages) == 0 {
			// No more eligible messages to delete.
			break
		}

		progress := 0
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
			progress++
		}

		// If a full page yielded no successful deletes (e.g. repeated delete
		// failures), stop to avoid an infinite loop.
		if progress == 0 {
			break
		}
	}

	if deleted > 0 {
		s.logger.Debug("enforced quota for user",
			"user_id", userID, "deleted", deleted, "excess", excess)
	}

	// If we could not delete enough eligible (old enough) messages, the user
	// remains over quota. Surface this so operators can adjust DeleteOlderThan
	// or investigate, rather than failing silently.
	if int64(deleted) < excess {
		s.logger.Warn("user still over quota after enforcement: insufficient eligible messages",
			"user_id", userID, "deleted", deleted, "excess", excess,
			"max_messages", policy.MaxMessages, "delete_older_than", policy.DeleteOlderThan)
	}

	return deleted, nil
}
