package mailbox_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/rbaliyan/event/v3/transport/channel"
	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// --- Test fakes for the attachment manager ---

// fakeMeta implements store.AttachmentMetadata.
type fakeMeta struct {
	id          string
	filename    string
	contentType string
	size        int64
	uri         string
	hash        string
	refCount    int
	createdAt   time.Time
}

func (m *fakeMeta) GetID() string          { return m.id }
func (m *fakeMeta) GetFilename() string     { return m.filename }
func (m *fakeMeta) GetContentType() string  { return m.contentType }
func (m *fakeMeta) GetSize() int64          { return m.size }
func (m *fakeMeta) GetURI() string          { return m.uri }
func (m *fakeMeta) GetCreatedAt() time.Time { return m.createdAt }
func (m *fakeMeta) GetHash() string         { return m.hash }
func (m *fakeMeta) GetRefCount() int        { return m.refCount }

// fakeMetadataStore is an in-memory store.AttachmentMetadataStore.
type fakeMetadataStore struct {
	mu     sync.Mutex
	byID   map[string]*fakeMeta
	byHash map[string]*fakeMeta
	nextID int
}

func newFakeMetadataStore() *fakeMetadataStore {
	return &fakeMetadataStore{
		byID:   make(map[string]*fakeMeta),
		byHash: make(map[string]*fakeMeta),
	}
}

func (s *fakeMetadataStore) Create(_ context.Context, data store.AttachmentCreate) (store.AttachmentMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	m := &fakeMeta{
		id:          string(rune('a'-1+s.nextID)) + "-meta",
		filename:    data.Filename,
		contentType: data.ContentType,
		size:        data.Size,
		uri:         data.URI,
		hash:        data.Hash,
		refCount:    0,
		createdAt:   time.Now(),
	}
	s.byID[m.id] = m
	if m.hash != "" {
		s.byHash[m.hash] = m
	}
	return m, nil
}

func (s *fakeMetadataStore) Get(_ context.Context, id string) (store.AttachmentMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return m, nil
}

func (s *fakeMetadataStore) GetByHash(_ context.Context, hash string) (store.AttachmentMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byHash[hash]
	if !ok {
		return nil, store.ErrNotFound
	}
	return m, nil
}

func (s *fakeMetadataStore) IncrementRef(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[id]
	if !ok {
		return store.ErrNotFound
	}
	m.refCount++
	return nil
}

func (s *fakeMetadataStore) DecrementRefAndDeleteIfZero(_ context.Context, id string) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[id]
	if !ok {
		return false, "", store.ErrNotFound
	}
	if m.refCount > 0 {
		m.refCount--
	}
	if m.refCount <= 0 {
		delete(s.byID, id)
		if m.hash != "" {
			delete(s.byHash, m.hash)
		}
		return true, m.uri, nil
	}
	return false, "", nil
}

func (s *fakeMetadataStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(s.byID, id)
	if m.hash != "" {
		delete(s.byHash, m.hash)
	}
	return nil
}

// fakeFileStore is an in-memory store.AttachmentFileStore.
type fakeFileStore struct {
	mu      sync.Mutex
	files   map[string][]byte
	nextURI int
}

func newFakeFileStore() *fakeFileStore {
	return &fakeFileStore{files: make(map[string][]byte)}
}

func (s *fakeFileStore) Upload(_ context.Context, _, _ string, content io.Reader) (string, error) {
	data, err := io.ReadAll(content)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextURI++
	uri := "mem://" + string(rune('a'-1+s.nextURI))
	s.files[uri] = data
	return uri, nil
}

func (s *fakeFileStore) Load(_ context.Context, uri string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[uri]
	if !ok {
		return nil, store.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeFileStore) Delete(_ context.Context, uri string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, uri)
	return nil
}

func (s *fakeFileStore) has(uri string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[uri]
	return ok
}

func readAllString(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	return string(b)
}

// TestAttachmentManager_Lifecycle exercises the full reference-counted
// lifecycle of the attachment manager: Upload, Load, GetMetadata, AddRef,
// RemoveRef, and deduplication by hash.
func TestAttachmentManager_Lifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("upload load and metadata", func(t *testing.T) {
		fs := newFakeFileStore()
		md := newFakeMetadataStore()
		mgr := mailbox.NewAttachmentManager(md, fs)

		meta, err := mgr.Upload(ctx, "doc.txt", "text/plain", "hash-1", bytes.NewBufferString("hello world"))
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if meta.GetFilename() != "doc.txt" {
			t.Errorf("filename = %q, want doc.txt", meta.GetFilename())
		}
		if meta.GetContentType() != "text/plain" {
			t.Errorf("content type = %q, want text/plain", meta.GetContentType())
		}
		if meta.GetRefCount() != 0 {
			t.Errorf("fresh upload ref count = %d, want 0", meta.GetRefCount())
		}

		// GetMetadata returns the same record.
		got, err := mgr.GetMetadata(ctx, meta.GetID())
		if err != nil {
			t.Fatalf("get metadata: %v", err)
		}
		if got.GetID() != meta.GetID() {
			t.Errorf("metadata id = %q, want %q", got.GetID(), meta.GetID())
		}

		// Load returns the uploaded bytes.
		rc, err := mgr.Load(ctx, meta.GetID())
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if content := readAllString(t, rc); content != "hello world" {
			t.Errorf("loaded content = %q, want %q", content, "hello world")
		}
	})

	t.Run("dedup by hash skips re-upload", func(t *testing.T) {
		fs := newFakeFileStore()
		md := newFakeMetadataStore()
		mgr := mailbox.NewAttachmentManager(md, fs)

		first, err := mgr.Upload(ctx, "a.txt", "text/plain", "dup-hash", bytes.NewBufferString("data"))
		if err != nil {
			t.Fatalf("first upload: %v", err)
		}
		second, err := mgr.Upload(ctx, "b.txt", "text/plain", "dup-hash", bytes.NewBufferString("data"))
		if err != nil {
			t.Fatalf("second upload: %v", err)
		}
		if first.GetID() != second.GetID() {
			t.Errorf("dedup failed: ids %q != %q", first.GetID(), second.GetID())
		}
		if len(fs.files) != 1 {
			t.Errorf("expected 1 stored file after dedup, got %d", len(fs.files))
		}
	})

	t.Run("addref then removeref deletes at zero", func(t *testing.T) {
		fs := newFakeFileStore()
		md := newFakeMetadataStore()
		mgr := mailbox.NewAttachmentManager(md, fs)

		meta, err := mgr.Upload(ctx, "ref.txt", "text/plain", "ref-hash", bytes.NewBufferString("payload"))
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		uri := meta.GetURI()

		// Two references.
		if err := mgr.AddRef(ctx, meta.GetID()); err != nil {
			t.Fatalf("add ref 1: %v", err)
		}
		if err := mgr.AddRef(ctx, meta.GetID()); err != nil {
			t.Fatalf("add ref 2: %v", err)
		}
		after, err := mgr.GetMetadata(ctx, meta.GetID())
		if err != nil {
			t.Fatalf("get metadata: %v", err)
		}
		if after.GetRefCount() != 2 {
			t.Fatalf("ref count = %d, want 2", after.GetRefCount())
		}

		// First release keeps the file.
		if err := mgr.RemoveRef(ctx, meta.GetID()); err != nil {
			t.Fatalf("remove ref 1: %v", err)
		}
		if !fs.has(uri) {
			t.Error("file removed prematurely after first release")
		}

		// Second release drops to zero and deletes both metadata and file.
		if err := mgr.RemoveRef(ctx, meta.GetID()); err != nil {
			t.Fatalf("remove ref 2: %v", err)
		}
		if fs.has(uri) {
			t.Error("file not deleted after final release")
		}
		if _, err := mgr.GetMetadata(ctx, meta.GetID()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("metadata should be gone, got err %v", err)
		}
	})

	t.Run("getmetadata missing returns not found", func(t *testing.T) {
		mgr := mailbox.NewAttachmentManager(newFakeMetadataStore(), newFakeFileStore())
		if _, err := mgr.GetMetadata(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("load missing metadata returns error", func(t *testing.T) {
		mgr := mailbox.NewAttachmentManager(newFakeMetadataStore(), newFakeFileStore())
		if _, err := mgr.Load(ctx, "missing"); err == nil {
			t.Error("expected error loading missing attachment")
		}
	})
}

// loadAttachAtt is a minimal store.Attachment for wiring into a sent message.
type loadAttachAtt struct {
	id  string
	uri string
}

func (a loadAttachAtt) GetID() string          { return a.id }
func (a loadAttachAtt) GetFilename() string     { return "file.txt" }
func (a loadAttachAtt) GetContentType() string  { return "text/plain" }
func (a loadAttachAtt) GetSize() int64          { return 7 }
func (a loadAttachAtt) GetURI() string          { return a.uri }
func (a loadAttachAtt) GetCreatedAt() time.Time { return time.Now() }

// newAttachSvc builds a connected service wired with the given attachment
// manager (and in-memory store + channel transport).
func newAttachSvc(t *testing.T, mgr store.AttachmentManager) mailbox.Service {
	t.Helper()
	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithAttachmentManager(mgr),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return svc
}

// firstInbox returns bob's first inbox message, retrying until delivery lands.
func firstInbox(t *testing.T, mb mailbox.Mailbox) mailbox.Message {
	t.Helper()
	var msg mailbox.Message
	mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
		list, err := mb.Folder(context.Background(), store.FolderInbox, store.ListOptions{Limit: 10})
		if err != nil || len(list.All()) == 0 {
			return false
		}
		msg = list.All()[0]
		return true
	}, "inbox copy never arrived")
	return msg
}

// TestMailbox_LoadAttachment covers the userMailbox.LoadAttachment path: a
// message carries an attachment, and the owner loads its content through the
// configured attachment manager.
func TestMailbox_LoadAttachment(t *testing.T) {
	ctx := context.Background()

	t.Run("not configured", func(t *testing.T) {
		svc := newTestService()
		defer svc.Close(ctx)
		alice := svc.Client("alice")
		bob := svc.Client("bob")
		msg, err := alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "hi",
			Body:         "body",
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		_, err = bob.LoadAttachment(ctx, msg.GetID(), "any")
		if !errors.Is(err, mailbox.ErrAttachmentStoreNotConfigured) {
			t.Errorf("expected ErrAttachmentStoreNotConfigured, got %v", err)
		}
	})

	t.Run("load and access checks", func(t *testing.T) {
		fs := newFakeFileStore()
		md := newFakeMetadataStore()
		mgr := mailbox.NewAttachmentManager(md, fs)

		// Pre-upload content and capture the generated metadata ID.
		meta, err := mgr.Upload(ctx, "file.txt", "text/plain", "h", bytes.NewBufferString("payload"))
		if err != nil {
			t.Fatalf("upload: %v", err)
		}

		svc := newAttachSvc(t, mgr)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		bob := svc.Client("bob")

		// Send a message carrying the attachment (ID matches the manager record).
		if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "with attachment",
			Body:         "see attached",
			Attachments:  []store.Attachment{loadAttachAtt{id: meta.GetID(), uri: meta.GetURI()}},
		}); err != nil {
			t.Fatalf("send: %v", err)
		}

		// Resolve bob's own inbox copy (he owns it).
		bobCopy := firstInbox(t, bob)

		// Owner can load the attachment content.
		rc, err := bob.LoadAttachment(ctx, bobCopy.GetID(), meta.GetID())
		if err != nil {
			t.Fatalf("load attachment: %v", err)
		}
		if content := readAllString(t, rc); content != "payload" {
			t.Errorf("content = %q, want payload", content)
		}

		// Unknown attachment ID on a known message is not found.
		if _, err := bob.LoadAttachment(ctx, bobCopy.GetID(), "nope"); !errors.Is(err, mailbox.ErrAttachmentNotFound) {
			t.Errorf("expected ErrAttachmentNotFound, got %v", err)
		}

		// A stranger cannot access bob's message copy.
		stranger := svc.Client("carol")
		if _, err := stranger.LoadAttachment(ctx, bobCopy.GetID(), meta.GetID()); err == nil {
			t.Error("expected error for unauthorized access")
		}
	})
}

// TestSendWithAttachmentIDs exercises ResolveAttachments + addAttachmentRefs by
// uploading attachments to a real manager and sending a message that references
// them by ID.
func TestSendWithAttachmentIDs(t *testing.T) {
	ctx := context.Background()
	fs := newFakeFileStore()
	md := newFakeMetadataStore()
	mgr := mailbox.NewAttachmentManager(md, fs)

	meta1, err := mgr.Upload(ctx, "a.txt", "text/plain", "h1", bytes.NewBufferString("aaa"))
	if err != nil {
		t.Fatalf("upload a: %v", err)
	}
	meta2, err := mgr.Upload(ctx, "b.txt", "text/plain", "h2", bytes.NewBufferString("bbb"))
	if err != nil {
		t.Fatalf("upload b: %v", err)
	}

	svc := newAttachSvc(t, mgr)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs:  []string{"bob"},
		Subject:       "with attachment ids",
		Body:          "body",
		AttachmentIDs: []string{meta1.GetID(), meta2.GetID()},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Refs were incremented for both attachments (sender + recipient copies).
	got1, _ := mgr.GetMetadata(ctx, meta1.GetID())
	if got1.GetRefCount() == 0 {
		t.Error("attachment 1 ref count not incremented after send")
	}

	// Resolving an unknown attachment ID errors.
	if _, err := alice.ResolveAttachments(ctx, []string{"unknown"}); err == nil {
		t.Error("expected error resolving unknown attachment")
	}

	// Resolving known IDs succeeds.
	resolved, err := alice.ResolveAttachments(ctx, []string{meta1.GetID(), meta2.GetID()})
	if err != nil {
		t.Fatalf("resolve attachments: %v", err)
	}
	if len(resolved) != 2 {
		t.Errorf("resolved %d attachments, want 2", len(resolved))
	}
}

// failAddRefManager fails AddRef to exercise the sender-copy rollback path.
type failAddRefManager struct {
	noopBase
	addRefErr error
}

type noopBase struct{}

func (noopBase) Upload(context.Context, string, string, string, io.Reader) (store.AttachmentMetadata, error) {
	return nil, nil
}
func (noopBase) Load(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (noopBase) GetMetadata(context.Context, string) (store.AttachmentMetadata, error) {
	return nil, nil
}
func (noopBase) RemoveRef(context.Context, string) error { return nil }

func (m failAddRefManager) AddRef(context.Context, string) error { return m.addRefErr }

// TestSend_AttachmentRefFailureRollback drives the createSenderMessage rollback
// path: adding attachment refs fails, so the sender copy is rolled back and the
// send returns an error.
func TestSend_AttachmentRefFailureRollback(t *testing.T) {
	ctx := context.Background()
	mgr := failAddRefManager{addRefErr: errors.New("ref store down")}
	svc := newAttachSvc(t, mgr)
	defer svc.Close(ctx)

	_, err := svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "with attachment",
		Body:         "body",
		Attachments:  []store.Attachment{loadAttachAtt{id: "att-x", uri: "mem://x"}},
	})
	if err == nil {
		t.Fatal("expected send to fail when attachment ref add fails")
	}
}
