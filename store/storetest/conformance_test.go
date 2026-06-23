package storetest_test

import (
	"context"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
	"github.com/rbaliyan/mailbox/store/storetest"
)

// newMemoryStore returns a freshly-connected in-memory store. Each call yields
// an isolated store with no shared state.
func newMemoryStore(t *testing.T) store.Store {
	t.Helper()
	s := memory.New()
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("memory store connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// TestMemoryConformance runs the full conformance suite against the in-memory
// backend. This is the local proof that the suite itself is correct: the memory
// backend is the well-tested reference implementation and must pass cleanly.
func TestMemoryConformance(t *testing.T) {
	storetest.RunStoreSuite(t, newMemoryStore)
}

// TestMemoryConcurrency runs the concurrency storms against the in-memory
// backend, validating its atomic operation guarantees under -race.
func TestMemoryConcurrency(t *testing.T) {
	storetest.RunConcurrencySuite(t, newMemoryStore)
}

// TestMemoryOutbox confirms the outbox suite correctly skips when the backend
// has no real outbox (the memory store reports OutboxEnabled() == false).
func TestMemoryOutbox(t *testing.T) {
	storetest.RunOutboxSuite(t, newMemoryStore)
}

// TestMemoryFailure runs the failure-mode suite against the in-memory backend.
// The in-memory store performs no context-aware I/O, so the suite records that
// behavioural gap via t.Log (visible with -v) while still proving the store
// never exposes torn state. Real database backends are asserted strictly under
// the integration build tag.
func TestMemoryFailure(t *testing.T) {
	storetest.RunFailureSuite(t, newMemoryStore)
}
