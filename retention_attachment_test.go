package mailbox

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// stubAttachment implements store.Attachment for testing.
type stubAttachment struct {
	id string
}

func (a stubAttachment) GetID() string            { return a.id }
func (a stubAttachment) GetFilename() string       { return "test.txt" }
func (a stubAttachment) GetContentType() string    { return "text/plain" }
func (a stubAttachment) GetSize() int64            { return 100 }
func (a stubAttachment) GetURI() string            { return "s3://bucket/" + a.id }
func (a stubAttachment) GetCreatedAt() time.Time   { return time.Now() }

// stubAttachmentManager implements store.AttachmentManager and tracks RemoveRef calls.
type stubAttachmentManager struct {
	mu         sync.Mutex
	removeRefs []string // attachment IDs passed to RemoveRef
}

func (m *stubAttachmentManager) Upload(_ context.Context, _, _, _ string, _ io.Reader) (store.AttachmentMetadata, error) {
	return nil, nil
}

func (m *stubAttachmentManager) Load(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, nil
}

func (m *stubAttachmentManager) GetMetadata(_ context.Context, _ string) (store.AttachmentMetadata, error) {
	return nil, nil
}

func (m *stubAttachmentManager) AddRef(_ context.Context, _ string) error {
	return nil
}

func (m *stubAttachmentManager) RemoveRef(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeRefs = append(m.removeRefs, id)
	return nil
}

func (m *stubAttachmentManager) removedRefs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.removeRefs))
	copy(result, m.removeRefs)
	return result
}

func TestCleanupExpiredMessages_ReleasesAttachmentRefs(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	attMgr := &stubAttachmentManager{}

	svc, err := NewService(
		WithStore(memStore),
		WithAttachmentManager(attMgr),
		WithMessageRetention(24*time.Hour),
	)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	// Create messages with attachments directly via the store.
	if err := memStore.Connect(ctx); err != nil && err != store.ErrAlreadyConnected {
		t.Fatalf("connect store: %v", err)
	}

	oldMsg, err := memStore.CreateMessage(ctx, store.MessageData{
		OwnerID:  "user1",
		SenderID: "sender",
		Subject:  "old with attachments",
		Body:     "body",
		FolderID: store.FolderInbox,
		Status:   store.MessageStatusSent,
		Attachments: []store.Attachment{
			stubAttachment{id: "att-1"},
			stubAttachment{id: "att-2"},
		},
	})
	if err != nil {
		t.Fatalf("create old message: %v", err)
	}

	// Create a fresh message with attachments (should NOT be cleaned up).
	_, err = memStore.CreateMessage(ctx, store.MessageData{
		OwnerID:  "user1",
		SenderID: "sender",
		Subject:  "fresh with attachments",
		Body:     "body",
		FolderID: store.FolderInbox,
		Status:   store.MessageStatusSent,
		Attachments: []store.Attachment{
			stubAttachment{id: "att-3"},
		},
	})
	if err != nil {
		t.Fatalf("create fresh message: %v", err)
	}

	// Create an old message WITHOUT attachments.
	_, err = memStore.CreateMessage(ctx, store.MessageData{
		OwnerID:  "user1",
		SenderID: "sender",
		Subject:  "old no attachments",
		Body:     "body",
		FolderID: store.FolderSent,
		Status:   store.MessageStatusSent,
	})
	if err != nil {
		t.Fatalf("create old no-att message: %v", err)
	}

	// Age only the old messages.
	memStore.AgeMessagesByID(48*time.Hour, oldMsg.GetID())
	// Also age the no-attachment message (it's the 3rd created, find its ID).
	msgs, err := memStore.Find(ctx, []store.Filter{store.InFolder(store.FolderSent)}, store.ListOptions{})
	if err != nil {
		t.Fatalf("find sent: %v", err)
	}
	for _, m := range msgs.Messages {
		memStore.AgeMessagesByID(48*time.Hour, m.GetID())
	}

	result, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// 2 old messages deleted (one with attachments, one without).
	if result.DeletedCount != 2 {
		t.Errorf("expected 2 deleted, got %d", result.DeletedCount)
	}

	// Only att-1 and att-2 should have refs released (from the old message).
	// att-3 should NOT be released (fresh message survived).
	refs := attMgr.removedRefs()
	if len(refs) != 2 {
		t.Fatalf("expected 2 RemoveRef calls, got %d: %v", len(refs), refs)
	}

	refSet := make(map[string]bool)
	for _, r := range refs {
		refSet[r] = true
	}
	if !refSet["att-1"] || !refSet["att-2"] {
		t.Errorf("expected RemoveRef for att-1 and att-2, got %v", refs)
	}
	if refSet["att-3"] {
		t.Error("att-3 should NOT have RemoveRef called (message survived)")
	}
}

func TestCleanupExpiredMessages_OnlyWinnerReleasesRefs(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	attMgr := &stubAttachmentManager{}

	svc, err := NewService(
		WithStore(memStore),
		WithAttachmentManager(attMgr),
		WithMessageRetention(24*time.Hour),
	)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	// Create a message with attachments.
	msg, err := memStore.CreateMessage(ctx, store.MessageData{
		OwnerID:  "user1",
		SenderID: "sender",
		Subject:  "test",
		Body:     "body",
		FolderID: store.FolderInbox,
		Status:   store.MessageStatusSent,
		Attachments: []store.Attachment{
			stubAttachment{id: "att-A"},
		},
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	memStore.AgeMessagesByID(48*time.Hour, msg.GetID())

	// First cleanup deletes and releases refs.
	result1, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if result1.DeletedCount != 1 {
		t.Errorf("first cleanup: expected 1 deleted, got %d", result1.DeletedCount)
	}

	refs1 := attMgr.removedRefs()
	if len(refs1) != 1 || refs1[0] != "att-A" {
		t.Errorf("first cleanup: expected [att-A], got %v", refs1)
	}

	// Second cleanup finds nothing to scan or delete — no additional ref releases.
	result2, err := svc.CleanupExpiredMessages(ctx)
	if err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if result2.DeletedCount != 0 {
		t.Errorf("second cleanup: expected 0 deleted, got %d", result2.DeletedCount)
	}

	refs2 := attMgr.removedRefs()
	if len(refs2) != 1 {
		t.Errorf("second cleanup should not add more refs, got %v", refs2)
	}
}

func TestCleanupTrash_ReleasesAttachmentRefs(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()
	attMgr := &stubAttachmentManager{}

	svc, err := NewService(
		WithStore(memStore),
		WithAttachmentManager(attMgr),
	)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	// Create a message with attachments in trash.
	msg, err := memStore.CreateMessage(ctx, store.MessageData{
		OwnerID:  "user1",
		SenderID: "sender",
		Subject:  "trashed",
		Body:     "body",
		FolderID: store.FolderTrash,
		Status:   store.MessageStatusSent,
		Attachments: []store.Attachment{
			stubAttachment{id: "trash-att-1"},
			stubAttachment{id: "trash-att-2"},
		},
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Age past default trash retention (30 days).
	memStore.AgeMessagesByID(31*24*time.Hour, msg.GetID())

	result, err := svc.CleanupTrash(ctx)
	if err != nil {
		t.Fatalf("cleanup trash: %v", err)
	}
	if result.DeletedCount != 1 {
		t.Errorf("expected 1 deleted, got %d", result.DeletedCount)
	}

	refs := attMgr.removedRefs()
	if len(refs) != 2 {
		t.Fatalf("expected 2 RemoveRef calls, got %d: %v", len(refs), refs)
	}

	refSet := make(map[string]bool)
	for _, r := range refs {
		refSet[r] = true
	}
	if !refSet["trash-att-1"] || !refSet["trash-att-2"] {
		t.Errorf("expected RemoveRef for trash-att-1 and trash-att-2, got %v", refs)
	}
}
