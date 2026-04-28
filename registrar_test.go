package mailbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/rbaliyan/mailbox/store/memory"
)

// fakeRegistrar is a simple Registrar implementation for tests.
type fakeRegistrar struct {
	id    string
	err   error
	calls int64
}

func (r *fakeRegistrar) Register(_ context.Context) (string, error) {
	atomic.AddInt64(&r.calls, 1)
	return r.id, r.err
}

var _ Registrar = (*fakeRegistrar)(nil)

func TestRegistrar(t *testing.T) {
	ctx := context.Background()

	t.Run("no registrar: Connect succeeds with empty MailboxID", func(t *testing.T) {
		svc, err := New(DefaultConfig(), WithStore(memory.New()))
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer svc.Close(ctx)

		if got := svc.MailboxID(); got != "" {
			t.Errorf("MailboxID() = %q, want empty", got)
		}
	})

	t.Run("registrar succeeds: Connect succeeds and MailboxID is assigned", func(t *testing.T) {
		reg := &fakeRegistrar{id: "mailbox-abc-123"}
		svc, err := New(DefaultConfig(),
			WithStore(memory.New()),
			WithRegistrar(reg),
		)
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer svc.Close(ctx)

		if got := atomic.LoadInt64(&reg.calls); got != 1 {
			t.Errorf("Register call count = %d, want 1", got)
		}
		if got := svc.MailboxID(); got != "mailbox-abc-123" {
			t.Errorf("MailboxID() = %q, want mailbox-abc-123", got)
		}
	})

	t.Run("registrar errors: Connect fails", func(t *testing.T) {
		regErr := errors.New("registry unreachable")
		reg := &fakeRegistrar{err: regErr}
		svc, err := New(DefaultConfig(),
			WithStore(memory.New()),
			WithRegistrar(reg),
		)
		if err != nil {
			t.Fatalf("create service: %v", err)
		}

		connectErr := svc.Connect(ctx)
		if connectErr == nil {
			t.Fatal("expected Connect to fail, got nil")
		}
		if !errors.Is(connectErr, regErr) {
			t.Errorf("expected Connect error to wrap regErr, got %v", connectErr)
		}

		// Service should not be connected after registration failure.
		if svc.IsConnected() {
			t.Error("service reports connected after Register failure")
		}
		if got := svc.MailboxID(); got != "" {
			t.Errorf("MailboxID() = %q after failure, want empty", got)
		}
	})

	t.Run("registrar returns empty ID: stored as empty", func(t *testing.T) {
		// Registrar chose not to assign an ID; that is the registrar's choice.
		// The service stores whatever Register returns.
		reg := &fakeRegistrar{id: ""}
		svc, err := New(DefaultConfig(),
			WithStore(memory.New()),
			WithRegistrar(reg),
		)
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer svc.Close(ctx)

		if got := svc.MailboxID(); got != "" {
			t.Errorf("MailboxID() = %q, want empty", got)
		}
	})

	t.Run("Register called exactly once per Connect", func(t *testing.T) {
		reg := &fakeRegistrar{id: "mb-1"}
		svc, err := New(DefaultConfig(),
			WithStore(memory.New()),
			WithRegistrar(reg),
		)
		if err != nil {
			t.Fatalf("create service: %v", err)
		}
		if err := svc.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer svc.Close(ctx)

		if got := atomic.LoadInt64(&reg.calls); got != 1 {
			t.Errorf("Register call count = %d, want 1", got)
		}
	})
}
