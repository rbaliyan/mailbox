package rules

import (
	"context"
	"fmt"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func setupStore(t *testing.T) store.Store {
	t.Helper()
	st := memory.New()
	if err := st.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	return st
}

func createTestMessage(t *testing.T, st store.Store) store.Message {
	t.Helper()
	msg, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:      "recipient1",
		SenderID:     "sender1",
		RecipientIDs: []string{"recipient1"},
		Subject:      "Test Invoice #123",
		Body:         "Please pay this invoice",
		Headers:      map[string]string{"X-Priority": "high"},
		Metadata:     map[string]any{"category": "billing"},
		Status:       store.MessageStatusDelivered,
		FolderID:     store.FolderInbox,
		Tags:         []string{"existing-tag"},
		ThreadID:     "thread-1",
		ReplyToID:    "parent-msg",
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestNewEngine(t *testing.T) {
	st := setupStore(t)
	provider := NewStaticProvider(nil, nil)

	t.Run("valid", func(t *testing.T) {
		e, err := NewEngine(provider, st)
		if err != nil {
			t.Fatal(err)
		}
		if e.Name() != "rules" {
			t.Errorf("Name() = %q, want %q", e.Name(), "rules")
		}
	})

	t.Run("nil provider", func(t *testing.T) {
		_, err := NewEngine(nil, st)
		if err == nil {
			t.Fatal("expected error for nil provider")
		}
	})

	t.Run("nil store", func(t *testing.T) {
		_, err := NewEngine(provider, nil)
		if err == nil {
			t.Fatal("expected error for nil store")
		}
	})
}

func TestCELEvaluation(t *testing.T) {
	st := setupStore(t)
	msg := createTestMessage(t, st)

	tests := []struct {
		name      string
		condition string
		want      bool
	}{
		{"match sender", `sender == "sender1"`, true},
		{"no match sender", `sender == "other"`, false},
		{"subject contains", `subject.contains("Invoice")`, true},
		{"subject not contains", `subject.contains("Report")`, false},
		{"has attachments false", `has_attachments`, false},
		{"attachment count", `attachment_count == 0`, true},
		{"is reply", `is_reply`, true},
		{"header match", `headers["X-Priority"] == "high"`, true},
		{"compound", `sender == "sender1" && subject.contains("Invoice")`, true},
		{"sender in list", `sender in ["sender1", "sender2"]`, true},
		{"tags check", `"existing-tag" in tags`, true},
		{"folder check", `folder == "__inbox"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewStaticProvider(nil, nil)
			e, err := NewEngine(provider, st)
			if err != nil {
				t.Fatal(err)
			}

			activation := buildActivation(msg)
			rule := Rule{ID: "test", Condition: tt.condition}
			got, err := e.evaluate(rule, activation)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("evaluate(%q) = %v, want %v", tt.condition, got, tt.want)
			}
		})
	}
}

func TestCELCompilationError(t *testing.T) {
	st := setupStore(t)
	provider := NewStaticProvider(nil, nil)
	e, err := NewEngine(provider, st)
	if err != nil {
		t.Fatal(err)
	}

	rule := Rule{ID: "bad", Condition: "invalid syntax !!!"}
	_, err = e.evaluate(rule, map[string]any{
		"sender":           "",
		"subject":          "",
		"body":             "",
		"recipients":       []string{},
		"headers":          map[string]string{},
		"metadata":         map[string]any{},
		"has_attachments":  false,
		"attachment_count": int64(0),
		"thread_id":        "",
		"is_reply":         false,
		"folder":           "",
		"tags":             []string{},
	})
	if err == nil {
		t.Fatal("expected compilation error")
	}
}

func TestReceiveActions(t *testing.T) {
	ctx := context.Background()

	t.Run("set folder", func(t *testing.T) {
		st := setupStore(t)
		msg := createTestMessage(t, st)

		provider := NewStaticProvider(nil, map[string][]Rule{
			"recipient1": {
				{
					ID:        "r1",
					Scope:     ScopeReceive,
					Condition: `subject.contains("Invoice")`,
					Actions:   []Action{{Type: ActionSetFolder, Value: "billing"}},
				},
			},
		})
		e, _ := NewEngine(provider, st)

		err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		})
		if err != nil {
			t.Fatal(err)
		}

		updated, _ := st.Get(ctx, msg.GetID())
		if updated.GetFolderID() != "billing" {
			t.Errorf("folder = %q, want %q", updated.GetFolderID(), "billing")
		}
	})

	t.Run("add tag", func(t *testing.T) {
		st := setupStore(t)
		msg := createTestMessage(t, st)

		provider := NewStaticProvider([]Rule{
			{
				ID:        "g1",
				Scope:     ScopeReceive,
				Condition: `sender == "sender1"`,
				Actions:   []Action{{Type: ActionAddTag, Value: "important"}},
			},
		}, nil)
		e, _ := NewEngine(provider, st)

		err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		})
		if err != nil {
			t.Fatal(err)
		}

		updated, _ := st.Get(ctx, msg.GetID())
		found := false
		for _, tag := range updated.GetTags() {
			if tag == "important" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tag 'important' not found in %v", updated.GetTags())
		}
	})

	t.Run("mark read", func(t *testing.T) {
		st := setupStore(t)
		msg := createTestMessage(t, st)

		provider := NewStaticProvider(nil, map[string][]Rule{
			"recipient1": {
				{
					ID:        "r1",
					Scope:     ScopeReceive,
					Condition: `sender == "sender1"`,
					Actions:   []Action{{Type: ActionMarkRead}},
				},
			},
		})
		e, _ := NewEngine(provider, st)

		err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		})
		if err != nil {
			t.Fatal(err)
		}

		updated, _ := st.Get(ctx, msg.GetID())
		if !updated.GetIsRead() {
			t.Error("message should be marked as read")
		}
	})

	t.Run("archive", func(t *testing.T) {
		st := setupStore(t)
		msg := createTestMessage(t, st)

		provider := NewStaticProvider(nil, map[string][]Rule{
			"recipient1": {
				{
					ID:        "r1",
					Scope:     ScopeReceive,
					Condition: "true",
					Actions:   []Action{{Type: ActionArchive}},
				},
			},
		})
		e, _ := NewEngine(provider, st)

		err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		})
		if err != nil {
			t.Fatal(err)
		}

		updated, _ := st.Get(ctx, msg.GetID())
		if updated.GetFolderID() != store.FolderArchived {
			t.Errorf("folder = %q, want %q", updated.GetFolderID(), store.FolderArchived)
		}
	})

	t.Run("spam", func(t *testing.T) {
		st := setupStore(t)
		msg := createTestMessage(t, st)

		provider := NewStaticProvider(nil, map[string][]Rule{
			"recipient1": {
				{
					ID:        "r1",
					Scope:     ScopeReceive,
					Condition: "true",
					Actions:   []Action{{Type: ActionSpam}},
				},
			},
		})
		e, _ := NewEngine(provider, st)

		err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		})
		if err != nil {
			t.Fatal(err)
		}

		updated, _ := st.Get(ctx, msg.GetID())
		if updated.GetFolderID() != store.FolderSpam {
			t.Errorf("folder = %q, want %q", updated.GetFolderID(), store.FolderSpam)
		}
	})

	t.Run("delete (soft)", func(t *testing.T) {
		st := setupStore(t)
		msg := createTestMessage(t, st)

		provider := NewStaticProvider(nil, map[string][]Rule{
			"recipient1": {
				{
					ID:        "r1",
					Scope:     ScopeReceive,
					Condition: "true",
					Actions:   []Action{{Type: ActionDelete}},
				},
			},
		})
		e, _ := NewEngine(provider, st)

		err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		})
		if err != nil {
			t.Fatal(err)
		}

		updated, _ := st.Get(ctx, msg.GetID())
		if updated.GetFolderID() != store.FolderTrash {
			t.Errorf("folder = %q, want %q (soft delete moves to trash)", updated.GetFolderID(), store.FolderTrash)
		}
	})

	t.Run("hard delete", func(t *testing.T) {
		st := setupStore(t)
		msg := createTestMessage(t, st)

		provider := NewStaticProvider(nil, map[string][]Rule{
			"recipient1": {
				{
					ID:        "r1",
					Scope:     ScopeReceive,
					Condition: "true",
					Actions:   []Action{{Type: ActionHardDelete}},
				},
			},
		})
		e, _ := NewEngine(provider, st)

		err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
			MessageID:   msg.GetID(),
			RecipientID: "recipient1",
		})
		if err != nil {
			t.Fatal(err)
		}

		_, err = st.Get(ctx, msg.GetID())
		if err == nil {
			t.Error("message should have been hard-deleted")
		}
	})
}

func TestPriorityOrdering(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)

	// Two rules match: priority 10 sets folder to "low-priority",
	// priority 1 sets folder to "high-priority". Since all rules apply
	// and last write wins, priority 10 (evaluated second) should win.
	provider := NewStaticProvider(nil, map[string][]Rule{
		"recipient1": {
			{
				ID:        "low",
				Scope:     ScopeReceive,
				Condition: "true",
				Actions:   []Action{{Type: ActionSetFolder, Value: "low-priority"}},
				Priority:  10,
			},
			{
				ID:        "high",
				Scope:     ScopeReceive,
				Condition: "true",
				Actions:   []Action{{Type: ActionSetFolder, Value: "high-priority"}},
				Priority:  1,
			},
		},
	})
	e, _ := NewEngine(provider, st)

	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	// Priority 1 evaluates first (high-priority), then priority 10 (low-priority).
	// Last write wins for folder.
	if updated.GetFolderID() != "low-priority" {
		t.Errorf("folder = %q, want %q (last write wins)", updated.GetFolderID(), "low-priority")
	}
}

func TestStopOnMatch(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)

	provider := NewStaticProvider(nil, map[string][]Rule{
		"recipient1": {
			{
				ID:          "first",
				Scope:       ScopeReceive,
				Condition:   "true",
				Actions:     []Action{{Type: ActionSetFolder, Value: "first-folder"}},
				Priority:    1,
				StopOnMatch: true,
			},
			{
				ID:        "second",
				Scope:     ScopeReceive,
				Condition: "true",
				Actions:   []Action{{Type: ActionSetFolder, Value: "second-folder"}},
				Priority:  2,
			},
		},
	})
	e, _ := NewEngine(provider, st)

	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	if updated.GetFolderID() != "first-folder" {
		t.Errorf("folder = %q, want %q (stop on match)", updated.GetFolderID(), "first-folder")
	}
}

func TestSendSideSkipsInvalidActions(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)

	// Create a message as if it's the sender's copy in sent folder.
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
				ID:    "send-rule",
				Scope: ScopeSend,
				Condition: "true",
				Actions: []Action{
					{Type: ActionAddTag, Value: "sent-tag"},
					{Type: ActionMarkRead},  // should be skipped
					{Type: ActionDelete},    // should be skipped
					{Type: ActionSpam},      // should be skipped
				},
			},
		},
	})
	e, _ := NewEngine(provider, st)

	err = e.AfterSend(ctx, "sender1", msg)
	if err != nil {
		t.Fatal(err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	// Tag should be added
	found := false
	for _, tag := range updated.GetTags() {
		if tag == "sent-tag" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("tag 'sent-tag' not found in %v", updated.GetTags())
	}

	// Message should NOT be deleted (ActionDelete skipped on send side)
	if updated.GetFolderID() != store.FolderSent {
		t.Errorf("folder = %q, want %q (delete/spam should be skipped)", updated.GetFolderID(), store.FolderSent)
	}
	if updated.GetIsRead() {
		t.Error("message should NOT be marked as read (MarkRead skipped on send side)")
	}
}

func TestScopeFiltering(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)

	// Only send-scope rules - should not match on receive
	provider := NewStaticProvider([]Rule{
		{
			ID:        "send-only",
			Scope:     ScopeSend,
			Condition: "true",
			Actions:   []Action{{Type: ActionSetFolder, Value: "should-not-apply"}},
		},
	}, nil)
	e, _ := NewEngine(provider, st)

	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	if updated.GetFolderID() != store.FolderInbox {
		t.Errorf("folder = %q, want %q (send-scope rules should not apply on receive)", updated.GetFolderID(), store.FolderInbox)
	}
}

func TestGlobalAndUserRulesCombined(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)

	provider := NewStaticProvider(
		[]Rule{
			{
				ID:        "global",
				Scope:     ScopeReceive,
				Condition: "true",
				Actions:   []Action{{Type: ActionAddTag, Value: "global-tag"}},
				Priority:  1,
			},
		},
		map[string][]Rule{
			"recipient1": {
				{
					ID:        "user",
					Scope:     ScopeReceive,
					Condition: "true",
					Actions:   []Action{{Type: ActionAddTag, Value: "user-tag"}},
					Priority:  2,
				},
			},
		},
	)
	e, _ := NewEngine(provider, st)

	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	tags := updated.GetTags()
	hasGlobal, hasUser := false, false
	for _, tag := range tags {
		if tag == "global-tag" {
			hasGlobal = true
		}
		if tag == "user-tag" {
			hasUser = true
		}
	}
	if !hasGlobal {
		t.Errorf("missing global-tag in %v", tags)
	}
	if !hasUser {
		t.Errorf("missing user-tag in %v", tags)
	}
}

func TestNoMatchingRules(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)

	provider := NewStaticProvider(nil, map[string][]Rule{
		"recipient1": {
			{
				ID:        "no-match",
				Scope:     ScopeReceive,
				Condition: `sender == "nonexistent"`,
				Actions:   []Action{{Type: ActionSetFolder, Value: "should-not-apply"}},
			},
		},
	})
	e, _ := NewEngine(provider, st)

	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	if updated.GetFolderID() != store.FolderInbox {
		t.Errorf("folder = %q, want %q", updated.GetFolderID(), store.FolderInbox)
	}
}

func TestStrictModeReturnsErrors(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)

	provider := NewStaticProvider(nil, map[string][]Rule{
		"recipient1": {
			{
				ID:        "bad-rule",
				Scope:     ScopeReceive,
				Condition: "invalid syntax !!!",
				Actions:   []Action{{Type: ActionMarkRead}},
			},
		},
	})
	e, _ := NewEngine(provider, st, WithStrictMode(true))

	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	})
	if err == nil {
		t.Error("expected error in strict mode for bad CEL expression")
	}
}

func TestNonStrictModeSkipsErrors(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)

	provider := NewStaticProvider(nil, map[string][]Rule{
		"recipient1": {
			{
				ID:        "bad-rule",
				Scope:     ScopeReceive,
				Condition: "invalid syntax !!!",
				Actions:   []Action{{Type: ActionMarkRead}},
				Priority:  1,
			},
			{
				ID:        "good-rule",
				Scope:     ScopeReceive,
				Condition: "true",
				Actions:   []Action{{Type: ActionAddTag, Value: "processed"}},
				Priority:  2,
			},
		},
	})
	e, _ := NewEngine(provider, st)

	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	})
	if err != nil {
		t.Fatalf("non-strict mode should not return error, got: %v", err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	found := false
	for _, tag := range updated.GetTags() {
		if tag == "processed" {
			found = true
			break
		}
	}
	if !found {
		t.Error("good rule should still apply after bad rule is skipped")
	}
}

func TestMultipleActionsOnSameRule(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st)

	provider := NewStaticProvider(nil, map[string][]Rule{
		"recipient1": {
			{
				ID:        "multi",
				Scope:     ScopeReceive,
				Condition: "true",
				Actions: []Action{
					{Type: ActionSetFolder, Value: "billing"},
					{Type: ActionAddTag, Value: "auto-filed"},
					{Type: ActionMarkRead},
				},
			},
		},
	})
	e, _ := NewEngine(provider, st)

	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "recipient1",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, _ := st.Get(ctx, msg.GetID())
	if updated.GetFolderID() != "billing" {
		t.Errorf("folder = %q, want %q", updated.GetFolderID(), "billing")
	}
	if !updated.GetIsRead() {
		t.Error("message should be marked as read")
	}
	found := false
	for _, tag := range updated.GetTags() {
		if tag == "auto-filed" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("tag 'auto-filed' not found in %v", updated.GetTags())
	}
}

func TestDefaultScopeIsReceive(t *testing.T) {
	r := Rule{ID: "test", Condition: "true"}
	if r.effectiveScope() != ScopeReceive {
		t.Errorf("effectiveScope() = %q, want %q", r.effectiveScope(), ScopeReceive)
	}
}

func TestPluginLifecycle(t *testing.T) {
	st := setupStore(t)
	provider := NewStaticProvider(nil, nil)
	e, _ := NewEngine(provider, st)

	if err := e.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBeforeSendNoOp(t *testing.T) {
	st := setupStore(t)
	provider := NewStaticProvider(nil, nil)
	e, _ := NewEngine(provider, st)

	err := e.BeforeSend(context.Background(), "user1", nil)
	if err != nil {
		t.Fatalf("BeforeSend should be no-op, got: %v", err)
	}
}

func TestCacheReuse(t *testing.T) {
	st := setupStore(t)
	provider := NewStaticProvider(nil, nil)
	e, _ := NewEngine(provider, st)

	rule := Rule{ID: "cached", Condition: `sender == "test"`}
	activation := map[string]any{
		"sender":           "test",
		"subject":          "",
		"body":             "",
		"recipients":       []string{},
		"headers":          map[string]string{},
		"metadata":         map[string]any{},
		"has_attachments":  false,
		"attachment_count": int64(0),
		"thread_id":        "",
		"is_reply":         false,
		"folder":           "",
		"tags":             []string{},
	}

	// Evaluate twice - second should use cache
	result1, err := e.evaluate(rule, activation)
	if err != nil {
		t.Fatal(err)
	}
	result2, err := e.evaluate(rule, activation)
	if err != nil {
		t.Fatal(err)
	}
	if result1 != result2 {
		t.Errorf("cached evaluation mismatch: %v != %v", result1, result2)
	}

	e.mu.RLock()
	cacheLen := len(e.cache)
	e.mu.RUnlock()
	if cacheLen != 1 {
		t.Errorf("cache length = %d, want 1", cacheLen)
	}
}

func TestOwnershipCheck(t *testing.T) {
	ctx := context.Background()
	st := setupStore(t)
	msg := createTestMessage(t, st) // owned by "recipient1"

	provider := NewStaticProvider([]Rule{
		{
			ID:        "r1",
			Scope:     ScopeReceive,
			Condition: "true",
			Actions:   []Action{{Type: ActionMarkRead}},
		},
	}, nil)
	e, _ := NewEngine(provider, st)

	// Try to apply rules as a different user than the message owner.
	err := e.OnMessageReceived(ctx, nil, MessageReceivedData{
		MessageID:   msg.GetID(),
		RecipientID: "attacker",
	})
	if err == nil {
		t.Fatal("expected ownership error")
	}

	// Message should remain unread.
	updated, _ := st.Get(ctx, msg.GetID())
	if updated.GetIsRead() {
		t.Error("message should NOT be marked as read by non-owner")
	}
}

func TestCacheEviction(t *testing.T) {
	st := setupStore(t)
	provider := NewStaticProvider(nil, nil)
	e, _ := NewEngine(provider, st, WithMaxCacheSize(3))

	activation := map[string]any{
		"sender":           "",
		"subject":          "",
		"body":             "",
		"recipients":       []string{},
		"headers":          map[string]string{},
		"metadata":         map[string]any{},
		"has_attachments":  false,
		"attachment_count": int64(0),
		"thread_id":        "",
		"is_reply":         false,
		"folder":           "",
		"tags":             []string{},
	}

	// Fill cache to capacity.
	for i := 0; i < 3; i++ {
		rule := Rule{ID: fmt.Sprintf("r%d", i), Condition: "true"}
		if _, err := e.evaluate(rule, activation); err != nil {
			t.Fatal(err)
		}
	}

	e.mu.RLock()
	if len(e.cache) != 3 {
		t.Errorf("cache length = %d, want 3", len(e.cache))
	}
	e.mu.RUnlock()

	// Add one more to trigger eviction.
	rule := Rule{ID: "overflow", Condition: "true"}
	if _, err := e.evaluate(rule, activation); err != nil {
		t.Fatal(err)
	}

	e.mu.RLock()
	if len(e.cache) != 1 {
		t.Errorf("cache length after eviction = %d, want 1", len(e.cache))
	}
	e.mu.RUnlock()
}

func TestConcurrentEvaluation(t *testing.T) {
	st := setupStore(t)

	provider := NewStaticProvider([]Rule{
		{
			ID:        "concurrent",
			Scope:     ScopeReceive,
			Condition: `sender == "sender1"`,
			Actions:   []Action{{Type: ActionAddTag, Value: "tagged"}},
		},
	}, nil)
	e, _ := NewEngine(provider, st)

	const goroutines = 20
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			ctx := context.Background()
			msg := createTestMessage(t, st)
			errs <- e.OnMessageReceived(ctx, nil, MessageReceivedData{
				MessageID:   msg.GetID(),
				RecipientID: "recipient1",
			})
		}()
	}

	for i := 0; i < goroutines; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent evaluation error: %v", err)
		}
	}
}
