package mailbox

import (
	"context"
	"errors"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// newListTestService returns a connected service backed by an in-memory store.
func newListTestService(t *testing.T) Service {
	t.Helper()
	svc, err := New(Config{}, WithStore(memory.New()))
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// seedInbox sends n messages from alice to bob and returns bob's mailbox.
func seedInbox(t *testing.T, svc Service, n int) Mailbox {
	t.Helper()
	ctx := context.Background()
	alice := svc.Client("alice")
	for i := 0; i < n; i++ {
		if _, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Subject",
			Body:         "Body",
		}); err != nil {
			t.Fatalf("send message %d: %v", i, err)
		}
	}
	return svc.Client("bob")
}

// bobInbox lists bob's inbox messages.
func bobInbox(t *testing.T, bob Mailbox) MessageList {
	t.Helper()
	list, err := bob.Folder(context.Background(), store.FolderInbox, store.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	return list
}

func TestMessageList_ReaderMethods(t *testing.T) {
	svc := newListTestService(t)
	bob := seedInbox(t, svc, 3)
	list := bobInbox(t, bob)

	if got := len(list.All()); got != 3 {
		t.Errorf("All() len = %d, want 3", got)
	}
	if got := list.Total(); got != 3 {
		t.Errorf("Total() = %d, want 3", got)
	}
	if list.HasMore() {
		t.Error("HasMore() = true, want false for a fully-returned page")
	}

	ml, ok := list.(*messageList)
	if !ok {
		t.Fatalf("Folder returned %T, want *messageList", list)
	}
	ids := ml.IDs()
	if len(ids) != 3 {
		t.Fatalf("IDs() len = %d, want 3", len(ids))
	}
	for i, id := range ids {
		if id == "" {
			t.Errorf("IDs()[%d] is empty", i)
		}
		if id != list.All()[i].GetID() {
			t.Errorf("IDs()[%d] = %q, want %q", i, id, list.All()[i].GetID())
		}
	}
	// NextCursor is whatever the store reports; just exercise the accessor.
	_ = list.NextCursor()
}

func TestMessageList_BulkMutations(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		// op runs the bulk mutation and returns its result.
		op func(t *testing.T, bob Mailbox, list MessageList) (*BulkResult, error)
		// verify checks the effect on a single re-fetched message.
		verify func(t *testing.T, bob Mailbox, msg Message)
		// wantErr reports whether op is expected to return a non-nil error.
		wantErr bool
	}{
		{
			name: "MarkRead",
			op: func(_ *testing.T, _ Mailbox, list MessageList) (*BulkResult, error) {
				return list.MarkRead(ctx)
			},
			verify: func(t *testing.T, _ Mailbox, msg Message) {
				if !msg.GetIsRead() {
					t.Errorf("message %s not marked read", msg.GetID())
				}
			},
		},
		{
			name: "MarkUnread",
			op: func(_ *testing.T, _ Mailbox, list MessageList) (*BulkResult, error) {
				// Mark read first so MarkUnread has an observable effect.
				if _, err := list.MarkRead(ctx); err != nil {
					return nil, err
				}
				return list.MarkUnread(ctx)
			},
			verify: func(t *testing.T, _ Mailbox, msg Message) {
				if msg.GetIsRead() {
					t.Errorf("message %s still marked read", msg.GetID())
				}
			},
		},
		{
			name: "Move",
			op: func(_ *testing.T, _ Mailbox, list MessageList) (*BulkResult, error) {
				return list.Move(ctx, store.FolderArchived)
			},
			verify: func(t *testing.T, _ Mailbox, msg Message) {
				if msg.GetFolderID() != store.FolderArchived {
					t.Errorf("message %s folder = %q, want %q", msg.GetID(), msg.GetFolderID(), store.FolderArchived)
				}
			},
		},
		{
			name: "Archive",
			op: func(_ *testing.T, _ Mailbox, list MessageList) (*BulkResult, error) {
				return list.(*messageList).Archive(ctx)
			},
			verify: func(t *testing.T, _ Mailbox, msg Message) {
				if msg.GetFolderID() != store.FolderArchived {
					t.Errorf("message %s folder = %q, want %q", msg.GetID(), msg.GetFolderID(), store.FolderArchived)
				}
			},
		},
		{
			name: "AddTag",
			op: func(_ *testing.T, _ Mailbox, list MessageList) (*BulkResult, error) {
				return list.AddTag(ctx, "important")
			},
			verify: func(t *testing.T, _ Mailbox, msg Message) {
				if !containsTag(msg.GetTags(), "important") {
					t.Errorf("message %s tags = %v, want to contain %q", msg.GetID(), msg.GetTags(), "important")
				}
			},
		},
		{
			name: "RemoveTag",
			op: func(_ *testing.T, _ Mailbox, list MessageList) (*BulkResult, error) {
				if _, err := list.AddTag(ctx, "temp"); err != nil {
					return nil, err
				}
				return list.RemoveTag(ctx, "temp")
			},
			verify: func(t *testing.T, _ Mailbox, msg Message) {
				if containsTag(msg.GetTags(), "temp") {
					t.Errorf("message %s tags = %v, still contains %q", msg.GetID(), msg.GetTags(), "temp")
				}
			},
		},
		{
			name: "Delete",
			op: func(_ *testing.T, _ Mailbox, list MessageList) (*BulkResult, error) {
				return list.Delete(ctx)
			},
			verify: func(t *testing.T, bob Mailbox, msg Message) {
				// After soft-delete the message lives in trash.
				got, err := bob.Get(ctx, msg.GetID())
				if err != nil {
					t.Fatalf("get after delete: %v", err)
				}
				if got.GetFolderID() != store.FolderTrash {
					t.Errorf("message %s folder = %q, want %q", msg.GetID(), got.GetFolderID(), store.FolderTrash)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newListTestService(t)
			bob := seedInbox(t, svc, 3)
			list := bobInbox(t, bob)
			ids := list.(*messageList).IDs()

			result, err := tc.op(t, bob, list)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.TotalCount() != 3 {
				t.Errorf("TotalCount() = %d, want 3", result.TotalCount())
			}
			if result.SuccessCount() != 3 {
				t.Errorf("SuccessCount() = %d, want 3", result.SuccessCount())
			}
			if result.HasFailures() {
				t.Errorf("HasFailures() = true, FailedIDs = %v", result.FailedIDs())
			}
			if got := len(result.SuccessfulIDs()); got != 3 {
				t.Errorf("SuccessfulIDs() len = %d, want 3", got)
			}
			// SuccessfulIDs must match the input IDs (order preserved).
			for i, id := range result.SuccessfulIDs() {
				if id != ids[i] {
					t.Errorf("SuccessfulIDs()[%d] = %q, want %q", i, id, ids[i])
				}
			}

			// Verify the store actually changed for each message.
			for _, id := range ids {
				msg, getErr := bob.Get(ctx, id)
				if tc.name == "Delete" {
					// Get still resolves trashed messages by ID.
					if getErr != nil {
						t.Fatalf("get %s: %v", id, getErr)
					}
				} else if getErr != nil {
					t.Fatalf("get %s: %v", id, getErr)
				}
				tc.verify(t, bob, msg)
			}
		})
	}
}

func TestMessageList_ForEachMessage_ContextCancelled(t *testing.T) {
	svc := newListTestService(t)
	bob := seedInbox(t, svc, 3)
	list := bobInbox(t, bob).(*messageList)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before iteration begins

	result, err := list.forEachMessage(ctx, func(_ Message) error { return nil })
	if err == nil {
		t.Fatal("expected an error from a cancelled context, got nil")
	}

	// Every message must be reported, all as failures carrying the ctx error.
	if result.TotalCount() != 3 {
		t.Errorf("TotalCount() = %d, want 3", result.TotalCount())
	}
	if result.SuccessCount() != 0 {
		t.Errorf("SuccessCount() = %d, want 0", result.SuccessCount())
	}
	for _, res := range result.Results {
		if !errors.Is(res.Error, context.Canceled) {
			t.Errorf("result %s error = %v, want context.Canceled", res.ID, res.Error)
		}
	}
}

func TestMessageList_ForEachMessage_CancelMidIteration(t *testing.T) {
	svc := newListTestService(t)
	bob := seedInbox(t, svc, 4)
	list := bobInbox(t, bob).(*messageList)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after the first message is processed; the rest must be reported
	// as cancelled by the per-iteration ctx.Err() check.
	var processed int
	result, err := list.forEachMessage(ctx, func(_ Message) error {
		processed++
		if processed == 1 {
			cancel()
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected an error after mid-iteration cancellation, got nil")
	}
	if processed != 1 {
		t.Errorf("processed = %d, want 1 before cancellation halted iteration", processed)
	}
	if result.SuccessCount() != 1 {
		t.Errorf("SuccessCount() = %d, want 1", result.SuccessCount())
	}
	if result.FailureCount() != 3 {
		t.Errorf("FailureCount() = %d, want 3 (the remaining items)", result.FailureCount())
	}
}

func containsTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}
