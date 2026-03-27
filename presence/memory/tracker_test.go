package memory

import (
	"context"
	"sync"
	"testing"

	"github.com/rbaliyan/mailbox/presence"
)

func TestRegisterAndIsOnline(t *testing.T) {
	tr := New()
	ctx := context.Background()

	online, err := tr.IsOnline(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if online {
		t.Fatal("expected user1 offline before registration")
	}

	reg, err := tr.Register(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}

	online, err = tr.IsOnline(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if !online {
		t.Fatal("expected user1 online after registration")
	}

	if err := reg.Unregister(ctx); err != nil {
		t.Fatal(err)
	}

	online, err = tr.IsOnline(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if online {
		t.Fatal("expected user1 offline after unregister")
	}
}

func TestMultipleRegistrations(t *testing.T) {
	tr := New()
	ctx := context.Background()

	reg1, _ := tr.Register(ctx, "user1")
	reg2, _ := tr.Register(ctx, "user1")

	online, _ := tr.IsOnline(ctx, "user1")
	if !online {
		t.Fatal("expected online with two registrations")
	}

	_ = reg1.Unregister(ctx)
	online, _ = tr.IsOnline(ctx, "user1")
	if !online {
		t.Fatal("expected still online with one registration remaining")
	}

	_ = reg2.Unregister(ctx)
	online, _ = tr.IsOnline(ctx, "user1")
	if online {
		t.Fatal("expected offline after all unregistered")
	}
}

func TestIdempotentUnregister(t *testing.T) {
	tr := New()
	ctx := context.Background()

	reg, _ := tr.Register(ctx, "user1")
	_ = reg.Unregister(ctx)
	_ = reg.Unregister(ctx) // Should not panic or double-decrement.

	online, _ := tr.IsOnline(ctx, "user1")
	if online {
		t.Fatal("expected offline")
	}
}

func TestOnlineUsers(t *testing.T) {
	tr := New()
	ctx := context.Background()

	reg1, _ := tr.Register(ctx, "a")
	_, _ = tr.Register(ctx, "b")
	_ = reg1.Unregister(ctx)

	online, err := tr.OnlineUsers(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(online) != 1 || online[0] != "b" {
		t.Fatalf("expected [b], got %v", online)
	}
}

func TestLocate(t *testing.T) {
	tr := New()
	ctx := context.Background()

	_, err := tr.Locate(ctx, "user1")
	if err != presence.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	_, _ = tr.Register(ctx, "user1", presence.WithRouting(presence.RoutingInfo{
		InstanceID: "inst-1",
		Metadata:   map[string]string{"port": "8080"},
	}))

	info, err := tr.Locate(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if info.InstanceID != "inst-1" {
		t.Fatalf("expected inst-1, got %s", info.InstanceID)
	}
	if info.Metadata["port"] != "8080" {
		t.Fatalf("expected port 8080, got %s", info.Metadata["port"])
	}
}

func TestLocateNoRouting(t *testing.T) {
	tr := New()
	ctx := context.Background()

	_, _ = tr.Register(ctx, "user1")

	info, err := tr.Locate(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if info.InstanceID != "" {
		t.Fatalf("expected empty routing, got %+v", info)
	}
}

func TestClosePreventsRegistration(t *testing.T) {
	tr := New()
	ctx := context.Background()

	_ = tr.Close(ctx)

	_, err := tr.Register(ctx, "user1")
	if err != presence.ErrTrackerClosed {
		t.Fatalf("expected ErrTrackerClosed, got %v", err)
	}

	_, err = tr.IsOnline(ctx, "user1")
	if err != presence.ErrTrackerClosed {
		t.Fatalf("expected ErrTrackerClosed, got %v", err)
	}
}

func TestConcurrentRegisterUnregister(t *testing.T) {
	tr := New()
	ctx := context.Background()

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)

	regs := make([]presence.Registration, n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			reg, err := tr.Register(ctx, "user1")
			if err != nil {
				t.Error(err)
				return
			}
			regs[i] = reg
		}(i)
	}
	wg.Wait()

	online, _ := tr.IsOnline(ctx, "user1")
	if !online {
		t.Fatal("expected online after concurrent registrations")
	}

	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			if regs[i] != nil {
				_ = regs[i].Unregister(ctx)
			}
		}(i)
	}
	wg.Wait()

	online, _ = tr.IsOnline(ctx, "user1")
	if online {
		t.Fatal("expected offline after all concurrent unregistrations")
	}
}
