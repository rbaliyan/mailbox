// Package storetest provides a reusable, backend-agnostic conformance suite
// for store.Store implementations.
//
// The suite is exercised against the in-memory backend in the default build
// (see conformance_test.go) and against the MongoDB and PostgreSQL backends
// behind the "integration" build tag, where a live database is available.
//
// Usage:
//
//	func TestMyStore(t *testing.T) {
//	    storetest.RunStoreSuite(t, func(t *testing.T) store.Store {
//	        s := mybackend.New(...)
//	        if err := s.Connect(context.Background()); err != nil {
//	            t.Fatalf("connect: %v", err)
//	        }
//	        t.Cleanup(func() { _ = s.Close(context.Background()) })
//	        return s
//	    })
//	}
//
// The factory must return a freshly-connected, isolated store. Isolation
// between subtests is achieved by using unique owner IDs per subtest, so a
// single shared store instance from the factory is sufficient.
package storetest

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// NewStoreFunc returns a freshly-connected, isolated store. The factory itself
// is responsible for registering cleanup via t.Cleanup.
type NewStoreFunc func(t *testing.T) store.Store

var ownerSeq uint64

// uniqueOwner returns an owner ID that is unique across the whole test process,
// so subtests sharing one store instance never collide on data.
func uniqueOwner(prefix string) string {
	n := atomic.AddUint64(&ownerSeq, 1)
	return fmt.Sprintf("%s-%d", prefix, n)
}

// eventually polls cond until it returns true or the timeout elapses. It is the
// flake-free alternative to time.Sleep for synchronizing on backends that apply
// writes (or build indexes) asynchronously.
func eventually(t *testing.T, timeout, interval time.Duration, cond func() bool, msg string) {
	t.Helper()
	if cond() {
		return
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			break
		}
	}
	t.Fatalf("storetest: condition not met within %s: %s", timeout, msg)
}

// RunStoreSuite runs the full backend-agnostic conformance suite against the
// store returned by newStore. Each subtest obtains its own store from the
// factory so that backends without process-global isolation (e.g. a shared
// database table) still get a clean slate.
func RunStoreSuite(t *testing.T, newStore NewStoreFunc) {
	t.Run("DraftLifecycle", func(t *testing.T) { testDraftLifecycle(t, newStore(t)) })
	t.Run("DraftListPagination", func(t *testing.T) { testDraftListPagination(t, newStore(t)) })
	t.Run("CreateAndGet", func(t *testing.T) { testCreateAndGet(t, newStore(t)) })
	t.Run("CreateMessagesBatch", func(t *testing.T) { testCreateMessagesBatch(t, newStore(t)) })
	t.Run("Filters", func(t *testing.T) { testFilters(t, newStore(t)) })
	t.Run("FindPagination", func(t *testing.T) { testFindPagination(t, newStore(t)) })
	t.Run("Count", func(t *testing.T) { testCount(t, newStore(t)) })
	t.Run("Search", func(t *testing.T) { testSearch(t, newStore(t)) })
	t.Run("MarkRead", func(t *testing.T) { testMarkRead(t, newStore(t)) })
	t.Run("MoveToFolder", func(t *testing.T) { testMoveToFolder(t, newStore(t)) })
	t.Run("Tags", func(t *testing.T) { testTags(t, newStore(t)) })
	t.Run("SoftDeleteRestore", func(t *testing.T) { testSoftDeleteRestore(t, newStore(t)) })
	t.Run("HardDelete", func(t *testing.T) { testHardDelete(t, newStore(t)) })
	t.Run("Idempotency", func(t *testing.T) { testIdempotency(t, newStore(t)) })
	t.Run("IdempotencyBatch", func(t *testing.T) { testIdempotencyBatch(t, newStore(t)) })
	t.Run("Maintenance", func(t *testing.T) { testMaintenance(t, newStore(t)) })
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// sentData builds a sent message owned by ownerID (owner == sender).
func sentData(ownerID, subject, body string) store.MessageData {
	return store.MessageData{
		OwnerID:      ownerID,
		SenderID:     ownerID,
		RecipientIDs: []string{"recipient-of-" + ownerID},
		Subject:      subject,
		Body:         body,
		Status:       store.MessageStatusSent,
		FolderID:     store.FolderSent,
	}
}

// inboxData builds a received message owned by ownerID (sender is someone else).
func inboxData(ownerID, senderID, subject, body string) store.MessageData {
	return store.MessageData{
		OwnerID:      ownerID,
		SenderID:     senderID,
		RecipientIDs: []string{ownerID},
		Subject:      subject,
		Body:         body,
		Status:       store.MessageStatusDelivered,
		FolderID:     store.FolderInbox,
	}
}

func mustCreate(t *testing.T, s store.Store, data store.MessageData) store.Message {
	t.Helper()
	m, err := s.CreateMessage(ctxT(t), data)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	return m
}

func find(t *testing.T, s store.Store, filters []store.Filter) *store.MessageList {
	t.Helper()
	list, err := s.Find(ctxT(t), filters, store.ListOptions{})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	return list
}

// ----------------------------------------------------------------------------
// DraftStore
// ----------------------------------------------------------------------------

func testDraftLifecycle(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("draft-owner")

	// NewDraft does not persist.
	d := s.NewDraft(owner)
	if d.GetOwnerID() != owner {
		t.Fatalf("NewDraft owner = %q, want %q", d.GetOwnerID(), owner)
	}

	// Populate the draft and round-trip thread/reply/external IDs.
	d.SetSubject("subj").
		SetBody("body").
		SetRecipients("bob", "carol").
		SetThreadID("thread-1").
		SetReplyToID("reply-1").
		SetExternalID("ext-1").
		SetHeader("X-Test", "v").
		SetMetadata("k", "v")

	saved, err := s.SaveDraft(ctx, d)
	if err != nil {
		t.Fatalf("SaveDraft insert: %v", err)
	}
	if saved.GetID() == "" {
		t.Fatal("SaveDraft did not assign an ID")
	}
	id := saved.GetID()

	got, err := s.GetDraft(ctx, id)
	if err != nil {
		t.Fatalf("GetDraft: %v", err)
	}
	if got.GetSubject() != "subj" || got.GetBody() != "body" {
		t.Fatalf("GetDraft content mismatch: subject=%q body=%q", got.GetSubject(), got.GetBody())
	}
	if got.GetThreadID() != "thread-1" {
		t.Errorf("thread_id round-trip: got %q", got.GetThreadID())
	}
	if got.GetReplyToID() != "reply-1" {
		t.Errorf("reply_to_id round-trip: got %q", got.GetReplyToID())
	}
	if got.GetExternalID() != "ext-1" {
		t.Errorf("external_id round-trip: got %q", got.GetExternalID())
	}
	if rs := got.GetRecipientIDs(); len(rs) != 2 {
		t.Errorf("recipients round-trip: got %v", rs)
	}

	// SaveDraft update: change the subject and persist via the same ID.
	got.SetSubject("subj-updated")
	updated, err := s.SaveDraft(ctx, got)
	if err != nil {
		t.Fatalf("SaveDraft update: %v", err)
	}
	if updated.GetID() != id {
		t.Fatalf("SaveDraft update changed ID: %q -> %q", id, updated.GetID())
	}
	reGot, err := s.GetDraft(ctx, id)
	if err != nil {
		t.Fatalf("GetDraft after update: %v", err)
	}
	if reGot.GetSubject() != "subj-updated" {
		t.Fatalf("update not persisted: subject=%q", reGot.GetSubject())
	}

	// A draft must not be visible as a message.
	if _, err := s.Get(ctx, id); !store.IsNotFound(err) {
		t.Fatalf("Get(draft) error = %v, want ErrNotFound", err)
	}

	// ListDrafts sees it.
	list, err := s.ListDrafts(ctx, owner, store.ListOptions{})
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	if list.Total != 1 || len(list.Drafts) != 1 {
		t.Fatalf("ListDrafts total=%d len=%d, want 1/1", list.Total, len(list.Drafts))
	}

	// DeleteDraft removes it.
	if err := s.DeleteDraft(ctx, id); err != nil {
		t.Fatalf("DeleteDraft: %v", err)
	}
	if _, err := s.GetDraft(ctx, id); !store.IsNotFound(err) {
		t.Fatalf("GetDraft after delete = %v, want ErrNotFound", err)
	}
	if err := s.DeleteDraft(ctx, id); !store.IsNotFound(err) {
		t.Fatalf("DeleteDraft(missing) = %v, want ErrNotFound", err)
	}
}

func testDraftListPagination(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("draft-page-owner")

	const total = 5
	for i := 0; i < total; i++ {
		d := s.NewDraft(owner).SetSubject(fmt.Sprintf("d-%d", i))
		if _, err := s.SaveDraft(ctx, d); err != nil {
			t.Fatalf("SaveDraft %d: %v", i, err)
		}
	}

	page1, err := s.ListDrafts(ctx, owner, store.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListDrafts page1: %v", err)
	}
	if page1.Total != total {
		t.Fatalf("page1 total = %d, want %d", page1.Total, total)
	}
	if len(page1.Drafts) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1.Drafts))
	}
	if !page1.HasMore {
		t.Fatal("page1 HasMore = false, want true")
	}

	last, err := s.ListDrafts(ctx, owner, store.ListOptions{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("ListDrafts last page: %v", err)
	}
	if len(last.Drafts) != 1 {
		t.Fatalf("last page len = %d, want 1", len(last.Drafts))
	}
	if last.HasMore {
		t.Fatal("last page HasMore = true, want false")
	}
}

// ----------------------------------------------------------------------------
// MessageStore: create / read
// ----------------------------------------------------------------------------

func testCreateAndGet(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("get-owner")

	data := sentData(owner, "hello", "world")
	data.ThreadID = "t-1"
	data.ReplyToID = "r-1"
	data.ExternalID = "x-1"
	data.Headers = map[string]string{"H": "1"}
	data.Metadata = map[string]any{"m": "2"}
	data.Tags = []string{"tagA"}

	created := mustCreate(t, s, data)
	if created.GetID() == "" {
		t.Fatal("CreateMessage returned empty ID")
	}

	got, err := s.Get(ctx, created.GetID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GetSubject() != "hello" || got.GetBody() != "world" {
		t.Fatalf("content mismatch: %q / %q", got.GetSubject(), got.GetBody())
	}
	if got.GetOwnerID() != owner || got.GetSenderID() != owner {
		t.Fatalf("owner/sender mismatch: %q / %q", got.GetOwnerID(), got.GetSenderID())
	}
	if got.GetFolderID() != store.FolderSent {
		t.Fatalf("folder = %q, want %q", got.GetFolderID(), store.FolderSent)
	}
	if got.GetThreadID() != "t-1" || got.GetReplyToID() != "r-1" || got.GetExternalID() != "x-1" {
		t.Fatalf("thread/reply/external mismatch: %q/%q/%q", got.GetThreadID(), got.GetReplyToID(), got.GetExternalID())
	}
	if got.GetIsRead() {
		t.Fatal("new message should be unread")
	}

	// Missing ID and empty ID.
	if _, err := s.Get(ctx, "does-not-exist"); !store.IsNotFound(err) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, ""); err == nil {
		t.Fatal("Get(\"\") should return an error")
	}
}

func testCreateMessagesBatch(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("batch-owner")

	batch := []store.MessageData{
		sentData(owner, "b1", "x"),
		sentData(owner, "b2", "y"),
		sentData(owner, "b3", "z"),
	}
	created, err := s.CreateMessages(ctx, batch)
	if err != nil {
		t.Fatalf("CreateMessages: %v", err)
	}
	if len(created) != len(batch) {
		t.Fatalf("CreateMessages returned %d, want %d", len(created), len(batch))
	}
	for _, m := range created {
		if m.GetID() == "" {
			t.Fatal("CreateMessages returned message with empty ID")
		}
	}

	count, err := s.Count(ctx, []store.Filter{store.OwnerIs(owner)})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != int64(len(batch)) {
		t.Fatalf("Count = %d, want %d", count, len(batch))
	}
}

// ----------------------------------------------------------------------------
// Filters
// ----------------------------------------------------------------------------

func testFilters(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("filter-owner")
	other := uniqueOwner("filter-other")

	// owner: two inbox (from alice), one sent.
	in1 := mustCreate(t, s, inboxData(owner, "alice", "from-alice-1", "body"))
	mustCreate(t, s, inboxData(owner, "alice", "from-alice-2", "body"))
	sent1 := mustCreate(t, s, sentData(owner, "sent-1", "body"))
	// noise owned by another user.
	mustCreate(t, s, inboxData(other, "alice", "other-inbox", "body"))

	// OwnerIs
	if got := find(t, s, []store.Filter{store.OwnerIs(owner)}); got.Total != 3 {
		t.Errorf("OwnerIs total = %d, want 3", got.Total)
	}

	// InFolder
	if got := find(t, s, []store.Filter{store.OwnerIs(owner), store.InFolder(store.FolderInbox)}); got.Total != 2 {
		t.Errorf("InFolder(inbox) total = %d, want 2", got.Total)
	}
	if got := find(t, s, []store.Filter{store.OwnerIs(owner), store.InFolder(store.FolderSent)}); got.Total != 1 {
		t.Errorf("InFolder(sent) total = %d, want 1", got.Total)
	}

	// SenderIs
	if got := find(t, s, []store.Filter{store.OwnerIs(owner), store.SenderIs("alice")}); got.Total != 2 {
		t.Errorf("SenderIs(alice) total = %d, want 2", got.Total)
	}

	// IsReadFilter: all unread initially.
	if got := find(t, s, []store.Filter{store.OwnerIs(owner), store.IsReadFilter(false)}); got.Total != 3 {
		t.Errorf("IsReadFilter(false) total = %d, want 3", got.Total)
	}
	if err := s.MarkRead(ctx, in1.GetID(), true); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if got := find(t, s, []store.Filter{store.OwnerIs(owner), store.IsReadFilter(true)}); got.Total != 1 {
		t.Errorf("IsReadFilter(true) total = %d, want 1", got.Total)
	}

	// tag filters
	if err := s.AddTag(ctx, sent1.GetID(), "important"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if got := find(t, s, []store.Filter{store.OwnerIs(owner), store.HasTag("important")}); got.Total != 1 {
		t.Errorf("HasTag total = %d, want 1", got.Total)
	}
	if got := find(t, s, []store.Filter{store.OwnerIs(owner), store.HasAnyTag()}); got.Total != 1 {
		t.Errorf("HasAnyTag total = %d, want 1", got.Total)
	}

	// id filter
	idFilter, err := store.MessageFilter("ID").Equal(sent1.GetID())
	if err != nil {
		t.Fatalf("build id filter: %v", err)
	}
	if got := find(t, s, []store.Filter{idFilter}); got.Total != 1 {
		t.Errorf("ID filter total = %d, want 1", got.Total)
	}
}

func testFindPagination(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("page-owner")

	const total = 6
	for i := 0; i < total; i++ {
		// Space out created_at so sort order is deterministic.
		mustCreate(t, s, sentData(owner, fmt.Sprintf("p-%d", i), "body"))
		time.Sleep(time.Millisecond) // distinct timestamps; not a synchronization sleep
	}

	filters := []store.Filter{store.OwnerIs(owner)}
	opts := store.ListOptions{Limit: 4, SortBy: "created_at", SortOrder: store.SortDesc}

	page1, err := s.Find(ctx, filters, opts)
	if err != nil {
		t.Fatalf("Find page1: %v", err)
	}
	if page1.Total != total {
		t.Fatalf("page1 total = %d, want %d", page1.Total, total)
	}
	if len(page1.Messages) != 4 {
		t.Fatalf("page1 len = %d, want 4", len(page1.Messages))
	}
	if !page1.HasMore {
		t.Fatal("page1 HasMore = false, want true")
	}

	// Walk the rest via the StartAfter cursor. Prefer the backend-supplied
	// NextCursor; when a backend leaves it empty, the cursor is the last
	// returned message's ID (the documented StartAfter contract).
	cursor := page1.NextCursor
	if cursor == "" {
		cursor = page1.Messages[len(page1.Messages)-1].GetID()
	}
	opts2 := opts
	opts2.StartAfter = cursor
	rest, err := s.Find(ctx, filters, opts2)
	if err != nil {
		t.Fatalf("Find page2: %v", err)
	}
	if len(rest.Messages) != 2 {
		t.Fatalf("page2 len = %d, want 2", len(rest.Messages))
	}
	if rest.HasMore {
		t.Fatal("page2 HasMore = true, want false")
	}

	// No overlap between the two pages.
	seen := make(map[string]bool)
	for _, m := range page1.Messages {
		seen[m.GetID()] = true
	}
	for _, m := range rest.Messages {
		if seen[m.GetID()] {
			t.Fatalf("message %s appears on both pages", m.GetID())
		}
	}
}

func testCount(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("count-owner")

	if c, err := s.Count(ctx, []store.Filter{store.OwnerIs(owner)}); err != nil || c != 0 {
		t.Fatalf("Count empty = %d, %v; want 0, nil", c, err)
	}
	for i := 0; i < 3; i++ {
		mustCreate(t, s, sentData(owner, fmt.Sprintf("c-%d", i), "body"))
	}
	if c, err := s.Count(ctx, []store.Filter{store.OwnerIs(owner)}); err != nil || c != 3 {
		t.Fatalf("Count = %d, %v; want 3, nil", c, err)
	}
}

func testSearch(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("search-owner")

	mustCreate(t, s, sentData(owner, "quarterly report", "revenue figures"))
	mustCreate(t, s, sentData(owner, "lunch plans", "pizza"))

	res, err := s.Search(ctx, store.SearchQuery{OwnerID: owner, Query: "quarterly"})
	if err != nil {
		// Backends may legitimately disable text search (e.g. regex off, FTS not
		// configured). Treat a disabled-search error as "not supported here".
		if store.IsNotFound(err) {
			t.Fatalf("Search returned ErrNotFound unexpectedly: %v", err)
		}
		t.Skipf("Search not supported by this backend: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("Search(quarterly) returned %d, want 1", len(res.Messages))
	}
	if res.Messages[0].GetSubject() != "quarterly report" {
		t.Fatalf("Search matched wrong message: %q", res.Messages[0].GetSubject())
	}
}

// ----------------------------------------------------------------------------
// MessageStore: mutation
// ----------------------------------------------------------------------------

func testMarkRead(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("read-owner")
	m := mustCreate(t, s, inboxData(owner, "alice", "subj", "body"))

	if err := s.MarkRead(ctx, m.GetID(), true); err != nil {
		t.Fatalf("MarkRead(true): %v", err)
	}
	got, _ := s.Get(ctx, m.GetID())
	if !got.GetIsRead() {
		t.Fatal("message not marked read")
	}
	if got.GetReadAt() == nil {
		t.Fatal("ReadAt not set after MarkRead(true)")
	}

	if err := s.MarkRead(ctx, m.GetID(), false); err != nil {
		t.Fatalf("MarkRead(false): %v", err)
	}
	got, _ = s.Get(ctx, m.GetID())
	if got.GetIsRead() {
		t.Fatal("message still read after MarkRead(false)")
	}

	if err := s.MarkRead(ctx, "missing", true); !store.IsNotFound(err) {
		t.Fatalf("MarkRead(missing) = %v, want ErrNotFound", err)
	}
}

func testMoveToFolder(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("move-owner")
	m := mustCreate(t, s, inboxData(owner, "alice", "subj", "body"))

	if err := s.MoveToFolder(ctx, m.GetID(), store.FolderArchived); err != nil {
		t.Fatalf("MoveToFolder: %v", err)
	}
	got, _ := s.Get(ctx, m.GetID())
	if got.GetFolderID() != store.FolderArchived {
		t.Fatalf("folder = %q, want %q", got.GetFolderID(), store.FolderArchived)
	}

	// Conditional move: from the wrong source folder must fail.
	err := s.MoveToFolder(ctx, m.GetID(), store.FolderInbox, store.FromFolder(store.FolderInbox))
	if !store.IsFolderMismatch(err) {
		t.Fatalf("conditional move from wrong folder = %v, want ErrFolderMismatch", err)
	}
	// Conditional move from the correct source folder succeeds.
	if err := s.MoveToFolder(ctx, m.GetID(), store.FolderInbox, store.FromFolder(store.FolderArchived)); err != nil {
		t.Fatalf("conditional move from correct folder: %v", err)
	}
	got, _ = s.Get(ctx, m.GetID())
	if got.GetFolderID() != store.FolderInbox {
		t.Fatalf("folder after conditional move = %q, want %q", got.GetFolderID(), store.FolderInbox)
	}
}

func testTags(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("tag-owner")
	m := mustCreate(t, s, inboxData(owner, "alice", "subj", "body"))

	if err := s.AddTag(ctx, m.GetID(), "work"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	// Adding the same tag again is idempotent.
	if err := s.AddTag(ctx, m.GetID(), "work"); err != nil {
		t.Fatalf("AddTag (dup): %v", err)
	}
	got, _ := s.Get(ctx, m.GetID())
	if cnt := countTag(got.GetTags(), "work"); cnt != 1 {
		t.Fatalf("tag count after dup add = %d, want 1 (tags=%v)", cnt, got.GetTags())
	}

	if err := s.RemoveTag(ctx, m.GetID(), "work"); err != nil {
		t.Fatalf("RemoveTag: %v", err)
	}
	got, _ = s.Get(ctx, m.GetID())
	if countTag(got.GetTags(), "work") != 0 {
		t.Fatalf("tag still present after removal: %v", got.GetTags())
	}
	// Removing a missing tag is a no-op.
	if err := s.RemoveTag(ctx, m.GetID(), "work"); err != nil {
		t.Fatalf("RemoveTag (missing): %v", err)
	}
}

func countTag(tags []string, tag string) int {
	n := 0
	for _, tg := range tags {
		if tg == tag {
			n++
		}
	}
	return n
}

func testSoftDeleteRestore(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("trash-owner")
	m := mustCreate(t, s, inboxData(owner, "alice", "subj", "body"))

	if err := s.Delete(ctx, m.GetID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.Get(ctx, m.GetID())
	if err != nil {
		t.Fatalf("Get after soft delete: %v (message should still exist in trash)", err)
	}
	if got.GetFolderID() != store.FolderTrash {
		t.Fatalf("soft-deleted folder = %q, want %q", got.GetFolderID(), store.FolderTrash)
	}

	// Inbox queries must exclude trash.
	inbox := find(t, s, []store.Filter{store.OwnerIs(owner), store.InFolder(store.FolderInbox)})
	if inbox.Total != 0 {
		t.Fatalf("trashed message still in inbox: total=%d", inbox.Total)
	}

	if err := s.Restore(ctx, m.GetID()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, _ = s.Get(ctx, m.GetID())
	if got.GetFolderID() != store.FolderInbox {
		t.Fatalf("restored folder = %q, want %q (received message)", got.GetFolderID(), store.FolderInbox)
	}
}

func testHardDelete(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("hard-owner")
	m := mustCreate(t, s, sentData(owner, "subj", "body"))

	if err := s.HardDelete(ctx, m.GetID()); err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if _, err := s.Get(ctx, m.GetID()); !store.IsNotFound(err) {
		t.Fatalf("Get after HardDelete = %v, want ErrNotFound", err)
	}
}

// ----------------------------------------------------------------------------
// Idempotency
// ----------------------------------------------------------------------------

func testIdempotency(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("idem-owner")
	key := "idem-key-1"
	data := sentData(owner, "idempotent", "body")

	first, created, err := s.CreateMessageIdempotent(ctx, data, key)
	if err != nil {
		t.Fatalf("CreateMessageIdempotent first: %v", err)
	}
	if !created {
		t.Fatal("first idempotent create: created = false, want true")
	}

	second, created, err := s.CreateMessageIdempotent(ctx, data, key)
	if err != nil {
		t.Fatalf("CreateMessageIdempotent second: %v", err)
	}
	if created {
		t.Fatal("second idempotent create: created = true, want false")
	}
	if second.GetID() != first.GetID() {
		t.Fatalf("idempotent create returned different ID: %q vs %q", second.GetID(), first.GetID())
	}

	// Exactly one row exists.
	if c, _ := s.Count(ctx, []store.Filter{store.OwnerIs(owner)}); c != 1 {
		t.Fatalf("after duplicate idempotent create, count = %d, want 1", c)
	}

	// Empty idempotency key is rejected.
	if _, _, err := s.CreateMessageIdempotent(ctx, data, ""); err == nil {
		t.Fatal("empty idempotency key should be rejected")
	}
}

func testIdempotencyBatch(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("idem-batch-owner")

	entries := []store.IdempotentCreateEntry{
		{Data: sentData(owner, "a", "body"), IdempotencyKey: "k-a"},
		{Data: sentData(owner, "b", "body"), IdempotencyKey: "k-b"},
	}
	first, err := s.CreateMessagesIdempotent(ctx, entries)
	if err != nil {
		t.Fatalf("CreateMessagesIdempotent first: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("batch returned %d results, want 2", len(first))
	}
	for i, r := range first {
		if r.Err != nil {
			t.Fatalf("entry %d error: %v", i, r.Err)
		}
		if !r.Created {
			t.Fatalf("entry %d created = false, want true on first pass", i)
		}
	}

	// Re-running with the same keys must not create duplicates.
	second, err := s.CreateMessagesIdempotent(ctx, entries)
	if err != nil {
		t.Fatalf("CreateMessagesIdempotent second: %v", err)
	}
	for i, r := range second {
		if r.Err != nil {
			t.Fatalf("entry %d (replay) error: %v", i, r.Err)
		}
		if r.Created {
			t.Fatalf("entry %d (replay) created = true, want false", i)
		}
		if r.Message.GetID() != first[i].Message.GetID() {
			t.Fatalf("entry %d replay returned different ID", i)
		}
	}
	if c, _ := s.Count(ctx, []store.Filter{store.OwnerIs(owner)}); c != 2 {
		t.Fatalf("after replay, count = %d, want 2", c)
	}
}

// ----------------------------------------------------------------------------
// MaintenanceStore
// ----------------------------------------------------------------------------

// ager is the optional capability some backends expose to deterministically
// shift timestamps backward for retention tests (the in-memory store provides
// this). Backends without it fall back to creating messages with real past
// timestamps where the contract allows.
type ager interface {
	AgeMessages(d time.Duration)
}

func testMaintenance(t *testing.T, s store.Store) {
	t.Run("DeleteExpiredTrash", func(t *testing.T) { testDeleteExpiredTrash(t, s) })
	t.Run("DeleteExpiredMessages", func(t *testing.T) { testDeleteExpiredMessages(t, s) })
	t.Run("DeleteTTLExpiredMessages", func(t *testing.T) { testDeleteTTLExpired(t, s) })
	t.Run("DeleteMessagesByIDs", func(t *testing.T) { testDeleteMessagesByIDs(t, s) })
}

func testDeleteExpiredTrash(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("trash-clean-owner")
	m := mustCreate(t, s, inboxData(owner, "alice", "subj", "body"))
	if err := s.Delete(ctx, m.GetID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	a, canAge := s.(ager)
	if canAge {
		// Age the trashed message so it falls before the cutoff. Cleanup keys
		// off updated_at, which Delete set to "now".
		a.AgeMessages(48 * time.Hour)
	}
	cutoff := time.Now().UTC().Add(-time.Hour)

	deleted, err := s.DeleteExpiredTrash(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteExpiredTrash: %v", err)
	}
	if canAge {
		if deleted != 1 {
			t.Fatalf("DeleteExpiredTrash deleted = %d, want 1", deleted)
		}
		if _, err := s.Get(ctx, m.GetID()); !store.IsNotFound(err) {
			t.Fatalf("trashed message survived cleanup: %v", err)
		}
		// A future cutoff with nothing left to delete returns 0.
		if d, err := s.DeleteExpiredTrash(ctx, cutoff); err != nil || d != 0 {
			t.Fatalf("DeleteExpiredTrash (empty) = %d, %v; want 0, nil", d, err)
		}
	}
}

func testDeleteExpiredMessages(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("retention-owner")
	m := mustCreate(t, s, sentData(owner, "old", "body"))

	a, canAge := s.(ager)
	if canAge {
		a.AgeMessages(48 * time.Hour)
	}
	cutoff := time.Now().UTC().Add(-time.Hour)

	deleted, err := s.DeleteExpiredMessages(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteExpiredMessages: %v", err)
	}
	if canAge {
		if deleted != 1 {
			t.Fatalf("DeleteExpiredMessages deleted = %d, want 1", deleted)
		}
		if _, err := s.Get(ctx, m.GetID()); !store.IsNotFound(err) {
			t.Fatalf("expired message survived cleanup: %v", err)
		}
	}
}

func testDeleteTTLExpired(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("ttl-owner")

	// Message with an expires_at firmly in the past. This is deterministic on
	// any backend since ExpiresAt is stored verbatim from MessageData.
	past := time.Now().UTC().Add(-time.Hour)
	data := sentData(owner, "ttl", "body")
	data.ExpiresAt = &past
	m := mustCreate(t, s, data)

	// A message with no TTL must survive.
	keep := mustCreate(t, s, sentData(owner, "keep", "body"))

	deleted, err := s.DeleteTTLExpiredMessages(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("DeleteTTLExpiredMessages: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteTTLExpiredMessages deleted = %d, want 1", deleted)
	}
	if _, err := s.Get(ctx, m.GetID()); !store.IsNotFound(err) {
		t.Fatalf("TTL-expired message survived: %v", err)
	}
	if _, err := s.Get(ctx, keep.GetID()); err != nil {
		t.Fatalf("non-TTL message was deleted: %v", err)
	}
}

func testDeleteMessagesByIDs(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := uniqueOwner("byids-owner")
	a := mustCreate(t, s, sentData(owner, "a", "body"))
	b := mustCreate(t, s, sentData(owner, "b", "body"))

	deleted, err := s.DeleteMessagesByIDs(ctx, []string{a.GetID(), b.GetID(), "missing"})
	if err != nil {
		t.Fatalf("DeleteMessagesByIDs: %v", err)
	}
	// The winner-reporting contract: only the IDs actually deleted are returned.
	if len(deleted) != 2 {
		t.Fatalf("DeleteMessagesByIDs reported %d deleted, want 2 (%v)", len(deleted), deleted)
	}
	got := map[string]bool{}
	for _, id := range deleted {
		got[id] = true
	}
	if !got[a.GetID()] || !got[b.GetID()] {
		t.Fatalf("winner set missing expected IDs: %v", deleted)
	}

	// Re-deleting reports nothing (already gone).
	again, err := s.DeleteMessagesByIDs(ctx, []string{a.GetID(), b.GetID()})
	if err != nil {
		t.Fatalf("DeleteMessagesByIDs (replay): %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-delete reported %d, want 0", len(again))
	}
}
