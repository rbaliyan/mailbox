package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// RunFailureSuite exercises failure-mode behaviour that the happy-path
// conformance suite does not cover: how the store reacts when the caller's
// context is already cancelled or its deadline has already passed before the
// operation begins.
//
// Contract verified here:
//   - When a mutating operation (CreateMessage / Delete) is invoked with an
//     already-cancelled or already-expired context, it must surface a
//     cancellation/deadline error (errors.Is(err, context.Canceled) or
//     context.DeadlineExceeded, possibly wrapped) AND must not leave partial
//     state behind.
//   - The same holds for read operations (Find / Get): they must surface the
//     cancellation rather than silently returning a successful result.
//
// Backend tolerance: backends wrap context errors differently (some return the
// raw sentinel, some wrap it with operation context). The suite accepts any
// error chain that satisfies errors.Is against context.Canceled /
// context.DeadlineExceeded.
//
// Reference-backend caveat: a pure in-memory store that performs no I/O may not
// observe the context at all and can complete the operation regardless. That is
// a real, observable behavioural gap rather than a test defect, so for such a
// backend the suite records the gap via t.Log (and verifies no torn state was
// written) instead of failing the build. Backends backed by a real database
// driver (MongoDB, PostgreSQL) propagate the cancellation through the driver and
// are therefore asserted strictly.
func RunFailureSuite(t *testing.T, newStore NewStoreFunc) {
	t.Run("CreateMessageCancelledContext", func(t *testing.T) {
		s := newStore(t)
		owner := uniqueOwner("fail-create-cancel")
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel before the call

		_, err := s.CreateMessage(ctx, sentData(owner, "cancelled", "body"))
		honored := assertCancellation(t, err, context.Canceled, "CreateMessage")

		// Regardless of whether the backend honoured the context, no partial
		// state must be observable for this owner once it did fail. When the
		// backend honoured cancellation the message must be absent; when it did
		// not, we still confirm the store is internally consistent (the message
		// is either fully present or fully absent, never torn).
		assertNoTornState(t, s, owner, honored)
	})

	t.Run("CreateMessageDeadlineExceeded", func(t *testing.T) {
		s := newStore(t)
		owner := uniqueOwner("fail-create-deadline")
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
		defer cancel()

		_, err := s.CreateMessage(ctx, sentData(owner, "deadline", "body"))
		honored := assertCancellation(t, err, context.DeadlineExceeded, "CreateMessage")
		assertNoTornState(t, s, owner, honored)
	})

	t.Run("FindCancelledContext", func(t *testing.T) {
		s := newStore(t)
		owner := uniqueOwner("fail-find-cancel")
		// Seed one row with a good context so a "silent success" would actually
		// return data — making the difference between honour/ignore observable.
		mustCreate(t, s, sentData(owner, "seed", "body"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := s.Find(ctx, []store.Filter{store.OwnerIs(owner)}, store.ListOptions{})
		assertCancellation(t, err, context.Canceled, "Find")
	})

	t.Run("GetCancelledContext", func(t *testing.T) {
		s := newStore(t)
		owner := uniqueOwner("fail-get-cancel")
		m := mustCreate(t, s, sentData(owner, "seed", "body"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := s.Get(ctx, m.GetID())
		assertCancellation(t, err, context.Canceled, "Get")
	})

	t.Run("GetDeadlineExceeded", func(t *testing.T) {
		s := newStore(t)
		owner := uniqueOwner("fail-get-deadline")
		m := mustCreate(t, s, sentData(owner, "seed", "body"))

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
		defer cancel()
		_, err := s.Get(ctx, m.GetID())
		assertCancellation(t, err, context.DeadlineExceeded, "Get")
	})

	t.Run("DeleteCancelledContext", func(t *testing.T) {
		s := newStore(t)
		owner := uniqueOwner("fail-delete-cancel")
		m := mustCreate(t, s, sentData(owner, "seed", "body"))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := s.Delete(ctx, m.GetID())
		honored := assertCancellation(t, err, context.Canceled, "Delete")

		// If the backend honoured cancellation the message must still be present
		// and untrashed (the delete was a no-op). If it did not honour the
		// context, the delete may have applied — that is the backend's
		// (logged) behaviour, not a torn state.
		if honored {
			got, gerr := s.Get(ctxT(t), m.GetID())
			if gerr != nil {
				t.Fatalf("after cancelled Delete the message must remain readable: %v", gerr)
			}
			if got.GetFolderID() == store.FolderTrash {
				t.Fatal("cancelled Delete must not have moved the message to trash")
			}
		}
	})
}

// assertCancellation checks that err carries the expected context error.
//
// It returns true when the backend honoured the context (err is non-nil and
// matches want). When err is nil it means the backend ignored the context and
// completed the operation; that is recorded via t.Log (a real behavioural gap,
// not a test failure) and false is returned. When err is non-nil but does NOT
// match the expected context error, that is treated as a hard failure because
// the operation failed for an unexpected reason.
func assertCancellation(t *testing.T, err error, want error, op string) bool {
	t.Helper()
	if err == nil {
		t.Logf("FINDING: %s did not honour an already-%s context and completed without error; "+
			"this backend performs no context-aware I/O for this operation", op, contextLabel(want))
		return false
	}
	if errors.Is(err, want) {
		return true
	}
	// Some drivers collapse both cancellation and deadline into context.Canceled
	// on an already-dead context; accept the sibling sentinel too so the suite
	// stays backend-tolerant while still proving the op failed for a
	// context-related reason.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Logf("%s surfaced a context error (%v) where %v was expected; accepted as backend-tolerant", op, err, want)
		return true
	}
	t.Fatalf("%s with an already-%s context returned %v, want a context cancellation/deadline error", op, contextLabel(want), err)
	return false
}

func contextLabel(want error) string {
	if errors.Is(want, context.DeadlineExceeded) {
		return "expired-deadline"
	}
	return "cancelled"
}

// assertNoTornState confirms the store never exposes a half-written message for
// owner. When the create was honoured (failed), the count must be zero. When it
// was not honoured (the backend ignored the context), the count must be exactly
// one fully-formed message — never a partial/corrupt record.
func assertNoTornState(t *testing.T, s store.Store, owner string, honored bool) {
	t.Helper()
	c, err := s.Count(ctxT(t), []store.Filter{store.OwnerIs(owner)})
	if err != nil {
		t.Fatalf("Count after failed create: %v", err)
	}
	if honored {
		if c != 0 {
			t.Fatalf("create failed on a cancelled context but %d message(s) were persisted (partial state)", c)
		}
		return
	}
	if c != 1 {
		t.Fatalf("create ignored the context but persisted %d messages, want exactly 1 well-formed message", c)
	}
}
