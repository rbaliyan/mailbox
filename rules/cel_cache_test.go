package rules

import (
	"context"
	"testing"

	"github.com/rbaliyan/mailbox/store"
)

// TestCachedCompile_HitMiss verifies that compiling the same regexp pattern
// twice reuses the cached *regexp.Regexp (cache hit) while a distinct pattern
// produces a different instance (cache miss).
func TestCachedCompile_HitMiss(t *testing.T) {
	const pattern = `(?i)^urgent-cache-test-[0-9]+$`

	first, err := cachedCompile(pattern)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	second, err := cachedCompile(pattern)
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if first != second {
		t.Error("expected cached regexp to be reused on the second call")
	}

	// A different pattern is a cache miss -> a distinct instance.
	other, err := cachedCompile(pattern + "x")
	if err != nil {
		t.Fatalf("other compile: %v", err)
	}
	if other == first {
		t.Error("distinct pattern unexpectedly shared a cached instance")
	}

	// Invalid patterns surface a compile error and are not cached.
	if _, err := cachedCompile("("); err == nil {
		t.Error("expected error for invalid pattern")
	}
}

// TestCustomCELFunctions exercises every registered custom CEL function so the
// customFunctions overloads are evaluated.
func TestCustomCELFunctions(t *testing.T) {
	st := setupStore(t)
	msg, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:      "recipient1",
		SenderID:     "sender1",
		RecipientIDs: []string{"recipient1"},
		Subject:      "URGENT: invoice",
		Body:         "body",
		Headers:      map[string]string{"X-Priority": "1"},
		Metadata:     map[string]any{"score": 7, "tenant": "acme"},
		Status:       store.MessageStatusDelivered,
		FolderID:     store.FolderInbox,
		Tags:         []string{"vip"},
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := NewStaticProvider(nil, nil)
	e, err := NewEngine(provider, st)
	if err != nil {
		t.Fatal(err)
	}
	activation := buildActivation(msg)

	tests := []struct {
		name      string
		condition string
		want      bool
	}{
		{"matches_regex hit", `matches_regex(subject, "(?i)urgent")`, true},
		{"matches_regex miss", `matches_regex(subject, "nope")`, false},
		{"matches_regex invalid", `matches_regex(subject, "(")`, false},
		{"has_header hit", `has_header(headers, "X-Priority")`, true},
		{"has_header miss", `has_header(headers, "X-Absent")`, false},
		{"header_value", `header_value(headers, "X-Priority") == "1"`, true},
		{"header_value missing", `header_value(headers, "X-None") == ""`, true},
		{"has_metadata hit", `has_metadata(metadata, "tenant")`, true},
		{"has_metadata miss", `has_metadata(metadata, "absent")`, false},
		{"metadata_value", `metadata_value(metadata, "tenant") == "acme"`, true},
		{"contains_tag hit", `contains_tag(tags, "vip")`, true},
		{"contains_tag miss", `contains_tag(tags, "none")`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.evaluate(Rule{ID: tt.name, Condition: tt.condition}, activation)
			if err != nil {
				t.Fatalf("evaluate(%q): %v", tt.condition, err)
			}
			if got != tt.want {
				t.Errorf("evaluate(%q) = %v, want %v", tt.condition, got, tt.want)
			}
		})
	}
}

// TestApplyAction_AllTypes drives every branch of applyAction via the store and
// verifies the resulting state.
func TestApplyAction_AllTypes(t *testing.T) {
	ctx := context.Background()

	newMsg := func(t *testing.T, st store.Store) string {
		t.Helper()
		m, err := st.CreateMessage(ctx, store.MessageData{
			OwnerID:      "recipient1",
			SenderID:     "sender1",
			RecipientIDs: []string{"recipient1"},
			Subject:      "subj",
			Body:         "body",
			Status:       store.MessageStatusDelivered,
			FolderID:     store.FolderInbox,
			Tags:         []string{"old"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return m.GetID()
	}

	t.Run("set folder", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionSetFolder, Value: "custom"}); err != nil {
			t.Fatal(err)
		}
		m, _ := st.Get(ctx, id)
		if m.GetFolderID() != "custom" {
			t.Errorf("folder = %q", m.GetFolderID())
		}
	})

	t.Run("add and remove tag", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionAddTag, Value: "new"}); err != nil {
			t.Fatal(err)
		}
		if err := e.applyAction(ctx, id, Action{Type: ActionRemoveTag, Value: "old"}); err != nil {
			t.Fatal(err)
		}
		m, _ := st.Get(ctx, id)
		var hasNew, hasOld bool
		for _, tg := range m.GetTags() {
			if tg == "new" {
				hasNew = true
			}
			if tg == "old" {
				hasOld = true
			}
		}
		if !hasNew || hasOld {
			t.Errorf("tags = %v, want [new] without [old]", m.GetTags())
		}
	})

	t.Run("mark read", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionMarkRead}); err != nil {
			t.Fatal(err)
		}
		m, _ := st.Get(ctx, id)
		if !m.GetIsRead() {
			t.Error("message not marked read")
		}
	})

	t.Run("delete", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionDelete}); err != nil {
			t.Fatal(err)
		}
		m, _ := st.Get(ctx, id)
		if m.GetFolderID() != store.FolderTrash {
			t.Errorf("folder = %q, want trash", m.GetFolderID())
		}
	})

	t.Run("hard delete", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionHardDelete}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Get(ctx, id); err == nil {
			t.Error("message still present after hard delete")
		}
	})

	t.Run("archive", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionArchive}); err != nil {
			t.Fatal(err)
		}
		m, _ := st.Get(ctx, id)
		if m.GetFolderID() != store.FolderArchived {
			t.Errorf("folder = %q, want archived", m.GetFolderID())
		}
	})

	t.Run("spam", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionSpam}); err != nil {
			t.Fatal(err)
		}
		m, _ := st.Get(ctx, id)
		if m.GetFolderID() != store.FolderSpam {
			t.Errorf("folder = %q, want spam", m.GetFolderID())
		}
	})

	t.Run("forward with forwarder", func(t *testing.T) {
		st := setupStore(t)
		fwd := &recordingForwarder{}
		e, _ := NewEngine(NewStaticProvider(nil, nil), st, WithForwarder(fwd))
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionForward, Value: "bob"}); err != nil {
			t.Fatal(err)
		}
		if !fwd.called || fwd.messageID != id || fwd.toUserID != "bob" {
			t.Errorf("forwarder = %+v, want called for %s->bob", fwd, id)
		}
	})

	t.Run("forward without forwarder is a warning no-op", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: ActionForward, Value: "bob"}); err != nil {
			t.Errorf("expected no error without forwarder, got %v", err)
		}
	})

	t.Run("unknown action lenient", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st)
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: "bogus"}); err != nil {
			t.Errorf("expected lenient no-op, got %v", err)
		}
	})

	t.Run("unknown action strict", func(t *testing.T) {
		st := setupStore(t)
		e, _ := NewEngine(NewStaticProvider(nil, nil), st, WithStrictMode(true))
		id := newMsg(t, st)
		if err := e.applyAction(ctx, id, Action{Type: "bogus"}); err == nil {
			t.Error("expected error in strict mode for unknown action")
		}
	})
}

// TestAfterSend_AppliesSendActions verifies AfterSend evaluates send-scope rules
// and applies only send-valid actions, honouring StopOnMatch.
func TestAfterSend_AppliesSendActions(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	msg, err := st.CreateMessage(ctx, store.MessageData{
		OwnerID:      "sender1",
		SenderID:     "sender1",
		RecipientIDs: []string{"recipient1"},
		Subject:      "Project Update",
		Body:         "body",
		Status:       store.MessageStatusSent,
		FolderID:     store.FolderSent,
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := NewStaticProvider(nil, map[string][]Rule{
		"sender1": {
			{
				ID:        "send-rule",
				Scope:     ScopeSend,
				Priority:  1,
				Condition: `subject.contains("Project")`,
				// ActionMarkRead is not a valid send action and must be skipped;
				// ActionAddTag is valid and must be applied.
				Actions: []Action{
					{Type: ActionMarkRead},
					{Type: ActionAddTag, Value: "sent-tagged"},
				},
				StopOnMatch: true,
			},
		},
	})
	e, _ := NewEngine(provider, st)

	if err := e.AfterSend(ctx, "sender1", msg); err != nil {
		t.Fatalf("AfterSend: %v", err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	var tagged bool
	for _, tg := range updated.GetTags() {
		if tg == "sent-tagged" {
			tagged = true
		}
	}
	if !tagged {
		t.Errorf("send action tag not applied: tags=%v", updated.GetTags())
	}
	// MarkRead is invalid for send scope, so it must have been skipped.
	if updated.GetIsRead() {
		t.Error("invalid send-scope ActionMarkRead was applied")
	}
}

// TestAfterSend_NoRules returns quickly when there are no send rules.
func TestAfterSend_NoRules(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)
	e, _ := NewEngine(NewStaticProvider(nil, nil), st)
	if err := e.AfterSend(ctx, "sender1", msg); err != nil {
		t.Fatalf("AfterSend with no rules: %v", err)
	}
}
