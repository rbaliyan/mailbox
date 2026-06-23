package rules

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rbaliyan/mailbox/store"
)

// slogDiscard returns a logger that discards all output.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestHTTPClient returns a distinct *http.Client for identity assertions.
func newTestHTTPClient() *http.Client {
	return &http.Client{}
}

// TestValidateWebhookURL exercises the SSRF guard's rejection branches. All
// cases are hermetic: they either fail to parse, use an unsupported scheme, or
// resolve to a loopback/private/link-local address without requiring external
// DNS. A public-resolving success case is intentionally omitted because it
// would depend on network access.
func TestValidateWebhookURL(t *testing.T) {
	tests := []struct {
		name      string
		rawURL    string
		wantSubst string // stable substring expected in the error, "" to skip
	}{
		{"empty", "", ""},
		{"scheme only", "://", ""},
		{"unsupported scheme ftp", "ftp://example.com", "unsupported scheme"},
		{"no scheme", "example.com/path", "unsupported scheme"},
		{"loopback ipv4", "http://127.0.0.1:8080/x", "internal address"},
		{"localhost resolves to loopback", "http://localhost/x", "internal address"},
		{"private 10.x", "http://10.0.0.1/x", "internal address"},
		{"private 192.168.x", "http://192.168.1.1/x", "internal address"},
		{"link-local 169.254.x", "http://169.254.169.254/latest", "internal address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWebhookURL(tt.rawURL)
			if err == nil {
				t.Fatalf("validateWebhookURL(%q) = nil, want error", tt.rawURL)
			}
			if tt.wantSubst != "" && !strings.Contains(err.Error(), tt.wantSubst) {
				t.Errorf("validateWebhookURL(%q) error = %q, want substring %q",
					tt.rawURL, err.Error(), tt.wantSubst)
			}
		})
	}
}

// TestEngineWebhook_Fires documents that webhook delivery against a loopback
// target (such as an httptest.Server, which listens on 127.0.0.1) is rejected
// by validateWebhookURL before any HTTP request is made. The webhook action is
// fire-and-forget, so AfterSend still returns nil — the engine logs a warning
// and skips delivery.
//
// This is a characterization test: validateWebhookURL's SSRF guard prevents a
// hermetic happy-path delivery test against httptest (loopback), and the guard
// runs before the HTTP client, so even a stub RoundTripper is never reached for
// a loopback/private URL.
func TestEngineWebhook_Fires(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	// A sender-owned copy in the sent folder. webhook is a valid send action.
	msg, err := st.CreateMessage(ctx, store.MessageData{
		OwnerID:      "sender1",
		SenderID:     "sender1",
		RecipientIDs: []string{"recipient1"},
		Subject:      "Test",
		Body:         "Hello",
		Status:       store.MessageStatusSent,
		FolderID:     store.FolderSent,
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := NewStaticProvider(nil, map[string][]Rule{
		"sender1": {
			{
				ID:        "wh",
				Scope:     ScopeSend,
				Condition: "true",
				// Loopback target: validateWebhookURL rejects it, so no
				// delivery occurs. Port 1 is also unreachable as a belt-and-
				// suspenders guard, but the URL check short-circuits first.
				Actions: []Action{{Type: ActionWebhook, Value: "http://127.0.0.1:1/x"}},
			},
		},
	})
	e, _ := NewEngine(provider, st)

	// Fire-and-forget: webhook delivery failures never abort the send.
	if err := e.AfterSend(ctx, "sender1", msg); err != nil {
		t.Fatalf("AfterSend with loopback webhook returned error, want nil: %v", err)
	}

	// The message is unchanged: webhook does not mutate the message.
	updated, _ := st.Get(ctx, msg.GetID())
	if updated.GetFolderID() != store.FolderSent {
		t.Errorf("folder = %q, want %q (webhook should not mutate message)",
			updated.GetFolderID(), store.FolderSent)
	}
}

// TestEngineWebhook_OnMessageReceived_Skips verifies that a receive-scope
// webhook rule pointing at a private address is skipped (fire-and-forget) and
// leaves the message unchanged.
func TestEngineWebhook_OnMessageReceived_Skips(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st) // owned by "recipient1", in __inbox

	provider := NewStaticProvider(nil, map[string][]Rule{
		"recipient1": {
			{
				ID:        "wh-recv",
				Scope:     ScopeReceive,
				Condition: "true",
				Actions:   []Action{{Type: ActionWebhook, Value: "http://10.0.0.1/hook"}},
			},
		},
	})
	e, _ := NewEngine(provider, st)

	if err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	}); err != nil {
		t.Fatalf("OnMessageReceived with private webhook returned error, want nil: %v", err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	if updated.GetFolderID() != store.FolderInbox {
		t.Errorf("folder = %q, want %q (webhook should not mutate message)",
			updated.GetFolderID(), store.FolderInbox)
	}
	if updated.GetIsRead() {
		t.Error("message should remain unread (webhook should not mutate message)")
	}
}

// TestCachedCompile_ReusesProgram verifies that compileOrCache returns the same
// compiled program on a second call with an identical (ID, Condition) key, and
// that the cache holds a single entry.
func TestCachedCompile_ReusesProgram(t *testing.T) {
	st := setupStore(t)
	provider := NewStaticProvider(nil, nil)
	e, _ := NewEngine(provider, st)

	rule := Rule{ID: "cached", Condition: `sender == "test"`}

	prg1, err := e.compileOrCache(rule)
	if err != nil {
		t.Fatal(err)
	}
	prg2, err := e.compileOrCache(rule)
	if err != nil {
		t.Fatal(err)
	}

	// cel.Program is an interface; identical cache hits return the same value.
	if prg1 != prg2 {
		t.Error("compileOrCache returned different programs for the same key; cache miss")
	}

	e.mu.RLock()
	cacheLen := len(e.cache)
	e.mu.RUnlock()
	if cacheLen != 1 {
		t.Errorf("cache length = %d, want 1", cacheLen)
	}
}

// recordingForwarder records the arguments of the last Forward call.
type recordingForwarder struct {
	called    bool
	messageID string
	toUserID  string
}

func (f *recordingForwarder) Forward(_ context.Context, messageID, toUserID string) error {
	f.called = true
	f.messageID = messageID
	f.toUserID = toUserID
	return nil
}

// TestEngineOptions verifies the engine option wiring: logger defaulting,
// forwarder enabling/disabling ActionForward, and HTTP client assignment.
func TestEngineOptions(t *testing.T) {
	st := setupStore(t)
	provider := NewStaticProvider(nil, nil)

	t.Run("WithLogger nil ignored", func(t *testing.T) {
		e, err := NewEngine(provider, st, WithLogger(nil))
		if err != nil {
			t.Fatal(err)
		}
		if e.logger == nil {
			t.Error("logger should remain the non-nil default when WithLogger(nil) is passed")
		}
	})

	t.Run("WithLogger custom sets logger", func(t *testing.T) {
		custom := slogDiscard()
		e, err := NewEngine(provider, st, WithLogger(custom))
		if err != nil {
			t.Fatal(err)
		}
		if e.logger != custom {
			t.Error("WithLogger(custom) did not set the custom logger")
		}
	})

	t.Run("WithForwarder enables ActionForward", func(t *testing.T) {
		ctx := context.Background()
		st := setupStore(t)
		msg := createTestMessage(t, st) // owned by "recipient1"

		fwd := &recordingForwarder{}
		provider := NewStaticProvider(nil, map[string][]Rule{
			"recipient1": {
				{
					ID:        "fwd-rule",
					Scope:     ScopeReceive,
					Condition: "true",
					Actions:   []Action{{Type: ActionForward, Value: "bob"}},
				},
			},
		})
		e, _ := NewEngine(provider, st, WithForwarder(fwd))

		if err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		}); err != nil {
			t.Fatal(err)
		}

		if !fwd.called {
			t.Fatal("Forward was not invoked")
		}
		if fwd.messageID != msg.GetID() {
			t.Errorf("Forward messageID = %q, want %q", fwd.messageID, msg.GetID())
		}
		if fwd.toUserID != "bob" {
			t.Errorf("Forward toUserID = %q, want %q", fwd.toUserID, "bob")
		}
	})

	t.Run("WithForwarder nil ignored", func(t *testing.T) {
		ctx := context.Background()
		st := setupStore(t)
		msg := createTestMessage(t, st)

		provider := NewStaticProvider(nil, map[string][]Rule{
			"recipient1": {
				{
					ID:        "fwd-rule",
					Scope:     ScopeReceive,
					Condition: "true",
					Actions:   []Action{{Type: ActionForward, Value: "bob"}},
				},
			},
		})
		// No forwarder configured: ActionForward logs and skips, returns nil.
		e, _ := NewEngine(provider, st, WithForwarder(nil))
		if e.opts.forwarder != nil {
			t.Error("WithForwarder(nil) should leave forwarder unset")
		}

		if err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		}); err != nil {
			t.Fatalf("ActionForward without forwarder should not error, got: %v", err)
		}
	})

	t.Run("WithHTTPClient sets client", func(t *testing.T) {
		custom := newTestHTTPClient()
		e, err := NewEngine(provider, st, WithHTTPClient(custom))
		if err != nil {
			t.Fatal(err)
		}
		if e.opts.httpClient != custom {
			t.Error("WithHTTPClient did not set opts.httpClient")
		}
	})

	t.Run("WithHTTPClient nil ignored", func(t *testing.T) {
		e, err := NewEngine(provider, st, WithHTTPClient(nil))
		if err != nil {
			t.Fatal(err)
		}
		if e.opts.httpClient != nil {
			t.Error("WithHTTPClient(nil) should leave httpClient unset")
		}
	})
}
