package mailbox

import (
	"context"
	"time"
)

// startBackgroundTasks launches goroutines for each non-zero cleanup interval
// in the Config. Each goroutine runs on a ticker and stops when the background
// context is cancelled (during Close).
func (s *service) startBackgroundTasks() {
	bgCtx, cancel := context.WithCancel(context.Background())
	s.bgCancel = cancel

	if s.cfg.TrashCleanupInterval > 0 {
		s.wg.Add(1)
		go s.runTrashCleanup(bgCtx)
		s.logger.Info("background trash cleanup enabled", "interval", s.cfg.TrashCleanupInterval)
	}

	if s.cfg.ExpiredMessageCleanupInterval > 0 {
		s.wg.Add(1)
		go s.runExpiredMessageCleanup(bgCtx)
		s.logger.Info("background expired message cleanup enabled", "interval", s.cfg.ExpiredMessageCleanupInterval)
	}

	if s.cfg.QuotaEnforcementInterval > 0 && s.cfg.QuotaUserLister != nil {
		s.wg.Add(1)
		go s.runQuotaEnforcement(bgCtx)
		s.logger.Info("background quota enforcement enabled", "interval", s.cfg.QuotaEnforcementInterval)
	}
}

// runTrashCleanup periodically runs CleanupTrash.
func (s *service) runTrashCleanup(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cfg.TrashCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := s.CleanupTrash(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.logger.Error("background trash cleanup failed", "error", err)
				continue
			}
			if result.DeletedCount > 0 {
				s.logger.Info("background trash cleanup completed", "deleted", result.DeletedCount)
			}
		}
	}
}

// runExpiredMessageCleanup periodically runs CleanupExpiredMessages.
func (s *service) runExpiredMessageCleanup(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cfg.ExpiredMessageCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := s.CleanupExpiredMessages(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.logger.Error("background expired message cleanup failed", "error", err)
				continue
			}
			if result.DeletedCount > 0 {
				s.logger.Info("background expired message cleanup completed", "deleted", result.DeletedCount)
			}
		}
	}
}

// runQuotaEnforcement periodically lists users via QuotaUserLister and runs EnforceQuotas.
func (s *service) runQuotaEnforcement(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cfg.QuotaEnforcementInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			userIDs, err := s.cfg.QuotaUserLister.ListUsers(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.logger.Error("background quota enforcement: failed to list users", "error", err)
				continue
			}
			if len(userIDs) == 0 {
				continue
			}
			result, err := s.EnforceQuotas(ctx, userIDs)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.logger.Error("background quota enforcement failed", "error", err)
				continue
			}
			if result.MessagesDeleted > 0 {
				s.logger.Info("background quota enforcement completed",
					"users_checked", result.UsersChecked,
					"users_over_quota", result.UsersOverQuota,
					"messages_deleted", result.MessagesDeleted)
			}
		}
	}
}
