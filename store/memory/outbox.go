package memory

import (
	"context"

	"github.com/rbaliyan/mailbox/store"
)

// Compile-time check.
var _ store.OutboxPersister = (*Store)(nil)

// OutboxEnabled returns false — memory store has no real outbox.
func (s *Store) OutboxEnabled() bool { return false }

// WithOutboxCtx calls fn directly — memory store has no transaction support.
func (s *Store) WithOutboxCtx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// PersistOutboxEvents is a no-op — memory store has no outbox.
func (s *Store) PersistOutboxEvents(_ context.Context, _ ...store.OutboxEvent) error {
	return nil
}
