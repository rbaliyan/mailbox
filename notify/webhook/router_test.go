package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/notify"
)

func TestVerifySignature(t *testing.T) {
	secret := []byte("super-secret")
	timestamp := "1700000000"
	body := []byte(`{"id":"1","type":"test"}`)

	valid := sign(secret, timestamp, body)
	sigHeader := "sha256=" + valid

	t.Run("valid signature passes", func(t *testing.T) {
		if !VerifySignature(secret, timestamp, body, sigHeader, 0) {
			t.Fatal("expected valid signature to verify")
		}
	})

	t.Run("tampered body fails", func(t *testing.T) {
		tampered := []byte(`{"id":"2","type":"test"}`)
		if VerifySignature(secret, timestamp, tampered, sigHeader, 0) {
			t.Fatal("expected tampered body to fail verification")
		}
	})

	t.Run("wrong key fails", func(t *testing.T) {
		if VerifySignature([]byte("other-secret"), timestamp, body, sigHeader, 0) {
			t.Fatal("expected wrong key to fail verification")
		}
	})

	t.Run("empty sigHeader fails", func(t *testing.T) {
		if VerifySignature(secret, timestamp, body, "", 0) {
			t.Fatal("expected empty sigHeader to fail verification")
		}
	})

	t.Run("prefix-only sigHeader fails", func(t *testing.T) {
		if VerifySignature(secret, timestamp, body, "sha256=", 0) {
			t.Fatal("expected prefix-only sigHeader to fail verification")
		}
	})

	t.Run("missing prefix fails", func(t *testing.T) {
		if VerifySignature(secret, timestamp, body, valid, 0) {
			t.Fatal("expected sigHeader without prefix to fail verification")
		}
	})

	t.Run("empty secret fails", func(t *testing.T) {
		if VerifySignature([]byte{}, timestamp, body, sigHeader, 0) {
			t.Fatal("expected empty secret to fail verification")
		}
	})

	t.Run("stale timestamp fails with tolerance", func(t *testing.T) {
		stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
		staleHeader := "sha256=" + sign(secret, stale, body)
		if VerifySignature(secret, stale, body, staleHeader, 5*time.Minute) {
			t.Fatal("expected stale timestamp to fail verification")
		}
	})

	t.Run("fresh timestamp passes with tolerance", func(t *testing.T) {
		now := strconv.FormatInt(time.Now().Unix(), 10)
		freshHeader := "sha256=" + sign(secret, now, body)
		if !VerifySignature(secret, now, body, freshHeader, 5*time.Minute) {
			t.Fatal("expected fresh timestamp to pass verification")
		}
	})
}

func TestMatches(t *testing.T) {
	r := &Router{opts: &options{}}

	t.Run("empty events matches any type", func(t *testing.T) {
		ep := EndpointConfig{URL: "http://x", Events: nil}
		if !r.matches(ep, "any.type") {
			t.Fatal("expected empty Events to match")
		}
	})

	t.Run("explicit type matches", func(t *testing.T) {
		ep := EndpointConfig{URL: "http://x", Events: []string{"mailbox.received", "mailbox.read"}}
		if !r.matches(ep, "mailbox.received") {
			t.Fatal("expected mailbox.received to match")
		}
	})

	t.Run("other types do not match", func(t *testing.T) {
		ep := EndpointConfig{URL: "http://x", Events: []string{"mailbox.received"}}
		if r.matches(ep, "mailbox.sent") {
			t.Fatal("expected mailbox.sent to be filtered out")
		}
	})
}

func TestRoute_Success(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Drain body so Close is clean
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := New(
		WithEndpoints(EndpointConfig{URL: srv.URL}),
		WithMaxRetries(0),
	)

	err := r.Route(context.Background(), notify.RoutingInfo{}, notify.Event{
		ID:   "1",
		Type: "test",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 hit, got %d", got)
	}
}

func TestRoute_AllFail(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := New(
		WithEndpoints(EndpointConfig{URL: srv.URL}),
		WithMaxRetries(2),
	)
	// Compress retry delays so the test is fast.
	r.opts.initDelay = time.Millisecond
	r.opts.maxDelay = time.Millisecond

	err := r.Route(context.Background(), notify.RoutingInfo{}, notify.Event{
		ID:   "1",
		Type: "test",
	})
	if err == nil {
		t.Fatal("expected error when all endpoints fail")
	}
	if !strings.Contains(err.Error(), "all 1 endpoints failed") {
		t.Fatalf("expected combined error message, got %q", err.Error())
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts (1 initial + 2 retries), got %d", got)
	}
}

func TestRoute_EventTypeMismatch(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := New(
		WithEndpoints(EndpointConfig{URL: srv.URL, Events: []string{"other.event"}}),
		WithMaxRetries(0),
	)

	err := r.Route(context.Background(), notify.RoutingInfo{}, notify.Event{
		ID:   "1",
		Type: "test",
	})
	if err != nil {
		t.Fatalf("expected nil error when no endpoints match, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("expected endpoint not to be called, got %d hits", got)
	}
}

func TestRoute_PartialSuccessReturnsNil(t *testing.T) {
	okHits, failHits := int32(0), int32(0)

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&okHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&failHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	r := New(
		WithEndpoints(
			EndpointConfig{URL: okSrv.URL},
			EndpointConfig{URL: failSrv.URL},
		),
		WithMaxRetries(0),
	)
	r.opts.initDelay = time.Millisecond
	r.opts.maxDelay = time.Millisecond

	err := r.Route(context.Background(), notify.RoutingInfo{}, notify.Event{
		ID:   "1",
		Type: "test",
	})
	if err != nil {
		t.Fatalf("expected nil error on partial success, got %v", err)
	}
	if atomic.LoadInt32(&okHits) == 0 {
		t.Fatal("expected OK endpoint to be called")
	}
	if atomic.LoadInt32(&failHits) == 0 {
		t.Fatal("expected failing endpoint to be called")
	}
}
