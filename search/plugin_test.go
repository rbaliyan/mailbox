package search_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	mailbox "github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/search"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// stubProvider is a test double for search.Provider.
type stubProvider struct {
	indexed    []search.Document
	deleted    []string
	searchIDs  []string
	searchErr  error
	connectErr error
	pingErr    error
	connectN   int
	pingN      int
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) Connect(_ context.Context) error {
	s.connectN++
	return s.connectErr
}
func (s *stubProvider) Ping(_ context.Context) error {
	s.pingN++
	return s.pingErr
}
func (s *stubProvider) Index(_ context.Context, doc search.Document) error {
	s.indexed = append(s.indexed, doc)
	return nil
}
func (s *stubProvider) Delete(_ context.Context, id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}
func (s *stubProvider) Search(_ context.Context, _ store.SearchQuery) ([]string, error) {
	return s.searchIDs, s.searchErr
}
func (s *stubProvider) Close(_ context.Context) error { return nil }

func TestNew_ReturnsPluginAndWrappedStore(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	stub := &stubProvider{}

	plugin, wrapped := search.New(stub, st)

	if plugin == nil {
		t.Fatal("expected non-nil plugin")
	}
	if wrapped == nil {
		t.Fatal("expected non-nil wrapped store")
	}
	if plugin.Name() != "search:stub" {
		t.Errorf("unexpected plugin name %q", plugin.Name())
	}
}

func TestInit_CallsConnectThenPing(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	stub := &stubProvider{}
	plugin, _ := search.New(stub, st)

	if err := plugin.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if stub.connectN != 1 {
		t.Errorf("Connect called %d times, want 1", stub.connectN)
	}
	if stub.pingN != 1 {
		t.Errorf("Ping called %d times, want 1", stub.pingN)
	}
}

func TestInit_ConnectError_PropagatesAndSkipsPing(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	want := errors.New("connect fail")
	stub := &stubProvider{connectErr: want}
	plugin, _ := search.New(stub, st)

	err := plugin.Init(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if stub.pingN != 0 {
		t.Error("Ping should not be called when Connect fails")
	}
}

func TestAfterSend_IndexesMessage(t *testing.T) {
	// Use an unconnected store; NewServiceWithStore will call Connect on it.
	st := memory.New()
	stub := &stubProvider{}
	plugin, wrappedStore := search.New(stub, st)

	svc := mailboxtest.NewServiceWithStore(t, mailbox.Config{}, wrappedStore, mailbox.WithPlugin(plugin))
	alice := svc.Client("alice")
	mailboxtest.SendMessage(t, alice, "bob", "Subject", "Body")

	if len(stub.indexed) == 0 {
		t.Fatal("expected at least one indexed document")
	}
	doc := stub.indexed[len(stub.indexed)-1]
	if doc.Subject != "Subject" {
		t.Errorf("indexed subject %q, want %q", doc.Subject, "Subject")
	}
}

func TestOnMessageReceived_IndexesMessage(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	stub := &stubProvider{}
	plugin, _ := search.New(stub, st)

	msg, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:  "bob",
		SenderID: "alice",
		Subject:  "Hello",
		Body:     "World",
		FolderID: store.FolderInbox,
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	err = plugin.OnMessageReceived(context.Background(), nil, mailbox.MessageReceivedEvent{
		MessageID: msg.GetID(),
	})
	if err != nil {
		t.Fatalf("OnMessageReceived: %v", err)
	}
	if len(stub.indexed) != 1 || stub.indexed[0].ID != msg.GetID() {
		t.Errorf("expected message %q indexed, got %v", msg.GetID(), stub.indexed)
	}
}

func TestOnMessageMoved_ReindexesWithNewFolder(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	stub := &stubProvider{}
	plugin, _ := search.New(stub, st)

	msg, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:  "bob",
		FolderID: store.FolderInbox,
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	if err := st.MoveToFolder(context.Background(), msg.GetID(), store.FolderArchived); err != nil {
		t.Fatalf("move: %v", err)
	}

	err = plugin.OnMessageMoved(context.Background(), nil, mailbox.MessageMovedEvent{
		MessageID:  msg.GetID(),
		UserID:     "bob",
		ToFolderID: store.FolderArchived,
	})
	if err != nil {
		t.Fatalf("OnMessageMoved: %v", err)
	}

	if len(stub.indexed) == 0 {
		t.Fatal("expected re-indexed document")
	}
	doc := stub.indexed[len(stub.indexed)-1]
	if doc.FolderID != store.FolderArchived {
		t.Errorf("re-indexed folder_id %q, want %q", doc.FolderID, store.FolderArchived)
	}
}

func TestOnMessageRead_ReindexesWithUpdatedIsRead(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	stub := &stubProvider{}
	plugin, _ := search.New(stub, st)

	msg, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:  "bob",
		FolderID: store.FolderInbox,
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}

	if err := st.MarkRead(context.Background(), msg.GetID(), true); err != nil {
		t.Fatalf("mark read: %v", err)
	}

	err = plugin.OnMessageRead(context.Background(), nil, mailbox.MessageReadEvent{
		MessageID: msg.GetID(),
		UserID:    "bob",
	})
	if err != nil {
		t.Fatalf("OnMessageRead: %v", err)
	}

	if len(stub.indexed) == 0 {
		t.Fatal("expected re-indexed document")
	}
	doc := stub.indexed[len(stub.indexed)-1]
	if !doc.IsRead {
		t.Error("re-indexed document should have IsRead=true")
	}
}

func TestOnMarkAllRead_ReindexesAllMessagesInFolder(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	stub := &stubProvider{}
	plugin, _ := search.New(stub, st)

	for i := 0; i < 3; i++ {
		if _, err := st.CreateMessage(context.Background(), store.MessageData{
			OwnerID:  "bob",
			FolderID: store.FolderInbox,
		}); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	err := plugin.OnMarkAllRead(context.Background(), nil, mailbox.MarkAllReadEvent{
		UserID:   "bob",
		FolderID: store.FolderInbox,
	})
	if err != nil {
		t.Fatalf("OnMarkAllRead: %v", err)
	}

	if len(stub.indexed) != 3 {
		t.Errorf("expected 3 re-indexed documents, got %d", len(stub.indexed))
	}
}

func TestOnDelete_RemovesFromIndex(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	stub := &stubProvider{}
	plugin, _ := search.New(stub, st)

	err := plugin.OnDelete(context.Background(), nil, mailbox.MessageDeletedEvent{
		MessageID: "msg-123",
	})
	if err != nil {
		t.Fatalf("OnDelete: %v", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != "msg-123" {
		t.Errorf("expected %q deleted, got %v", "msg-123", stub.deleted)
	}
}

func TestSearch_FallsBackOnProviderError(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	stub := &stubProvider{searchErr: errors.New("provider down")}
	_, wrappedStore := search.New(stub, st)

	// Seed a message directly in the primary store.
	if _, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:  "alice",
		Subject:  "fallback",
		FolderID: store.FolderInbox,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	list, err := wrappedStore.Search(context.Background(), store.SearchQuery{
		OwnerID: "alice",
		Query:   "fallback",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// The fallback should return the message from the primary store.
	if len(list.Messages) == 0 {
		t.Error("expected fallback to return results from primary store")
	}
}

func TestSearch_NoFallback_ReturnsError(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)
	wantErr := errors.New("provider down")
	stub := &stubProvider{searchErr: wantErr}
	_, wrappedStore := search.New(stub, st, search.WithFallback(false))

	_, err := wrappedStore.Search(context.Background(), store.SearchQuery{OwnerID: "alice"})
	if !errors.Is(err, wantErr) {
		t.Errorf("got %v, want %v", err, wantErr)
	}
}

func TestSearch_OwnershipMismatch_Filtered(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)

	// Create a message owned by "bob".
	msg, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:  "bob",
		FolderID: store.FolderInbox,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Provider returns bob's message ID for alice's query.
	stub := &stubProvider{searchIDs: []string{msg.GetID()}}
	_, wrappedStore := search.New(stub, st)

	list, err := wrappedStore.Search(context.Background(), store.SearchQuery{OwnerID: "alice"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(list.Messages) != 0 {
		t.Errorf("ownership mismatch should filter out message, got %d results", len(list.Messages))
	}
}

func TestSearch_MissingIDLogged_NoError(t *testing.T) {
	st := mailboxtest.NewMemoryStore(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	stub := &stubProvider{searchIDs: []string{"nonexistent-id"}}
	_, wrappedStore := search.New(stub, st, search.WithLogger(logger))

	list, err := wrappedStore.Search(context.Background(), store.SearchQuery{OwnerID: "alice"})
	if err != nil {
		t.Fatalf("Search should not return error on missing ID: %v", err)
	}
	if len(list.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(list.Messages))
	}
	if buf.Len() == 0 {
		t.Error("expected warning log for missing ID")
	}
}
