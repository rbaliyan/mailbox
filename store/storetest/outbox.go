package storetest

import (
	"context"
	"errors"
	"testing"

	event "github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/mailbox/store"
)

// RunOutboxSuite validates the transactional outbox contract. It runs only when
// the store reports OutboxEnabled() == true; otherwise it skips. Pass a factory
// that builds the store with outbox enabled (e.g. WithOutbox(true)).
//
// The store-layer outbox contract verified here:
//   - OutboxEnabled() reports true.
//   - EventOutboxProvider exposes a non-nil event.OutboxStore for the relay.
//   - WithOutboxCtx wraps fn in a transaction whose context carries the outbox
//     transaction marker (event.InOutboxTx), so Event.Publish() inside fn routes
//     to the outbox rather than the transport.
//   - On success the wrapped mutation is committed and visible.
//   - On error the transaction rolls back and no mutation is persisted.
func RunOutboxSuite(t *testing.T, newStore NewStoreFunc) {
	s := newStore(t)

	op, ok := s.(store.OutboxPersister)
	if !ok || !op.OutboxEnabled() {
		t.Skip("store does not have the outbox enabled")
	}

	t.Run("EventOutboxStoreAvailable", func(t *testing.T) {
		provider, ok := s.(store.EventOutboxProvider)
		if !ok {
			t.Fatal("outbox-enabled store does not implement EventOutboxProvider")
		}
		if provider.EventOutboxStore() == nil {
			t.Fatal("EventOutboxStore() returned nil while outbox is enabled")
		}
	})

	t.Run("WithOutboxCtxMarksTransaction", func(t *testing.T) {
		ctx := ctxT(t)
		var sawTx bool
		err := op.WithOutboxCtx(ctx, func(txCtx context.Context) error {
			sawTx = event.InOutboxTx(txCtx)
			return nil
		})
		if err != nil {
			t.Fatalf("WithOutboxCtx: %v", err)
		}
		if !sawTx {
			t.Fatal("context inside WithOutboxCtx did not carry the outbox transaction marker")
		}
	})

	t.Run("CommitPersistsMutation", func(t *testing.T) {
		ctx := ctxT(t)
		owner := uniqueOwner("outbox-commit-owner")
		var created store.Message
		err := op.WithOutboxCtx(ctx, func(txCtx context.Context) error {
			m, err := s.CreateMessage(txCtx, sentData(owner, "committed", "body"))
			if err != nil {
				return err
			}
			created = m
			return nil
		})
		if err != nil {
			t.Fatalf("WithOutboxCtx commit: %v", err)
		}
		if _, err := s.Get(ctx, created.GetID()); err != nil {
			t.Fatalf("committed message not visible: %v", err)
		}
	})

	t.Run("RollbackDiscardsMutation", func(t *testing.T) {
		ctx := ctxT(t)
		owner := uniqueOwner("outbox-rollback-owner")
		sentinel := errors.New("forced rollback")
		err := op.WithOutboxCtx(ctx, func(txCtx context.Context) error {
			if _, err := s.CreateMessage(txCtx, sentData(owner, "rolled-back", "body")); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithOutboxCtx error = %v, want sentinel", err)
		}
		// The mutation must not survive the rollback.
		c, err := s.Count(ctx, []store.Filter{store.OwnerIs(owner)})
		if err != nil {
			t.Fatalf("Count after rollback: %v", err)
		}
		if c != 0 {
			t.Fatalf("after rollback, count = %d, want 0 (transaction did not roll back)", c)
		}
	})
}
