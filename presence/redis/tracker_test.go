package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rbaliyan/mailbox/presence"
	goredis "github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*Tracker, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	tr := New(client, WithTTL(5*time.Second), WithRefreshInterval(1*time.Second))
	return tr, mr
}

func TestRegisterAndIsOnline(t *testing.T) {
	tr, _ := setup(t)
	ctx := context.Background()

	online, err := tr.IsOnline(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if online {
		t.Fatal("expected offline before registration")
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
		t.Fatal("expected online after registration")
	}

	if err := reg.Unregister(ctx); err != nil {
		t.Fatal(err)
	}

	online, err = tr.IsOnline(ctx, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if online {
		t.Fatal("expected offline after unregister")
	}
}

func TestMultipleRegistrations(t *testing.T) {
	tr, _ := setup(t)
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
		t.Fatal("expected still online with one remaining")
	}

	_ = reg2.Unregister(ctx)
	online, _ = tr.IsOnline(ctx, "user1")
	if online {
		t.Fatal("expected offline after all unregistered")
	}
}

func TestIdempotentUnregister(t *testing.T) {
	tr, _ := setup(t)
	ctx := context.Background()

	reg, _ := tr.Register(ctx, "user1")
	_ = reg.Unregister(ctx)
	_ = reg.Unregister(ctx) // Should not panic or error.

	online, _ := tr.IsOnline(ctx, "user1")
	if online {
		t.Fatal("expected offline")
	}
}

func TestOnlineUsers(t *testing.T) {
	tr, _ := setup(t)
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

func TestLocateWithRouting(t *testing.T) {
	tr, _ := setup(t)
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

func TestLocateWithoutRouting(t *testing.T) {
	tr, _ := setup(t)
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

func TestTTLExpiry(t *testing.T) {
	tr, mr := setup(t)
	ctx := context.Background()

	_, _ = tr.Register(ctx, "user1")

	online, _ := tr.IsOnline(ctx, "user1")
	if !online {
		t.Fatal("expected online")
	}

	// Fast-forward past TTL.
	mr.FastForward(10 * time.Second)

	online, _ = tr.IsOnline(ctx, "user1")
	if online {
		t.Fatal("expected offline after TTL expiry")
	}
}

func TestClosePreventsRegistration(t *testing.T) {
	tr, _ := setup(t)
	ctx := context.Background()

	_ = tr.Close(ctx)

	_, err := tr.Register(ctx, "user1")
	if err != presence.ErrTrackerClosed {
		t.Fatalf("expected ErrTrackerClosed, got %v", err)
	}
}

func TestCloseCancelsRefreshLoops(t *testing.T) {
	tr, mr := setup(t)
	ctx := context.Background()

	_, _ = tr.Register(ctx, "user1")
	_, _ = tr.Register(ctx, "user2")

	_ = tr.Close(ctx)

	// After close, refresh loops are cancelled.
	// Fast-forward past TTL — keys should expire without refresh.
	mr.FastForward(10 * time.Second)

	// Direct Redis check — keys should be gone.
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()

	n, _ := client.Exists(ctx, "presence:user1").Result()
	if n != 0 {
		t.Fatal("expected user1 key to expire after close")
	}
	n, _ = client.Exists(ctx, "presence:user2").Result()
	if n != 0 {
		t.Fatal("expected user2 key to expire after close")
	}
}

func TestConcurrentRegisterUnregister(t *testing.T) {
	tr, _ := setup(t)
	ctx := context.Background()

	const n = 50
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
		t.Fatal("expected online")
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
		t.Fatal("expected offline after all unregistered")
	}
}
