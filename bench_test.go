package mailbox

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// benchSink defends benchmark results (Stats, Search, Get, …) from dead-code
// elimination. The compiler cannot prove the assigned value is unused, so the
// measured work is never optimized away.
var benchSink any

// noBulkBenchStore embeds store.Store (which does NOT include store.BulkUpdater),
// so a service built on it cannot take the native bulk fast path and is forced
// onto the paginated fallback in the filter-based bulk operations. This mirrors
// the noBulkStore wrapper used in the package's external tests, duplicated here
// because bench_test.go is in package mailbox (internal).
type noBulkBenchStore struct {
	store.Store
}

// newNoBulkBenchService builds a connected service whose store hides
// BulkUpdater, forcing the paginated fallback path.
func newNoBulkBenchService(b *testing.B) *service {
	b.Helper()
	inner := memory.New()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := New(Config{}, WithStore(&noBulkBenchStore{Store: inner}), WithLogger(silent))
	if err != nil {
		b.Fatalf("new service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		b.Fatalf("connect: %v", err)
	}
	b.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc.(*service)
}

// newBenchService creates a minimal connected service backed by an in-memory store.
// Logging is discarded to keep benchmark output clean.
func newBenchService(b *testing.B) *service {
	b.Helper()
	st := memory.New()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := New(Config{}, WithStore(st), WithLogger(silent))
	if err != nil {
		b.Fatalf("new service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		b.Fatalf("connect: %v", err)
	}
	b.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc.(*service)
}

// seedMessages sends n messages from "sender" to "owner" and returns their IDs.
func seedMessages(b *testing.B, svc *service, owner string, n int) []string {
	b.Helper()
	ids := make([]string, 0, n)
	mb := svc.Client(owner)
	for i := range n {
		msg, err := mb.SendMessage(context.Background(), SendRequest{
			RecipientIDs: []string{owner},
			Subject:      fmt.Sprintf("Subject %d", i),
			Body:         fmt.Sprintf("Body content for message number %d with some extra text", i),
		})
		if err != nil {
			b.Fatalf("seed message %d: %v", i, err)
		}
		ids = append(ids, msg.GetID())
	}
	return ids
}

// BenchmarkSendMessage measures single-message delivery (1 recipient).
func BenchmarkSendMessage(b *testing.B) {
	svc := newBenchService(b)
	mb := svc.Client("alice")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := mb.SendMessage(context.Background(), SendRequest{
			RecipientIDs: []string{"alice"},
			Subject:      "Hello",
			Body:         "World",
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSendMessage_Recipients measures delivery across different recipient counts.
func BenchmarkSendMessage_Recipients(b *testing.B) {
	for _, n := range []int{1, 5, 10, 25} {
		b.Run(fmt.Sprintf("recipients=%d", n), func(b *testing.B) {
			svc := newBenchService(b)
			mb := svc.Client("sender")
			recipients := make([]string, n)
			for i := range n {
				recipients[i] = fmt.Sprintf("user%d", i)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := mb.SendMessage(context.Background(), SendRequest{
					RecipientIDs: recipients,
					Subject:      "Broadcast",
					Body:         "Hello everyone",
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkGet measures single-message retrieval by ID.
func BenchmarkGet(b *testing.B) {
	svc := newBenchService(b)
	ids := seedMessages(b, svc, "bob", 100)
	mb := svc.Client("bob")
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		msg, err := mb.Get(context.Background(), ids[i%len(ids)])
		if err != nil {
			b.Fatal(err)
		}
		benchSink = msg
		i++
	}
}

// BenchmarkInbox measures inbox listing across different page sizes and inbox depths.
func BenchmarkInbox(b *testing.B) {
	for _, depth := range []int{100, 1000} {
		for _, limit := range []int{10, 50, 100} {
			if limit > depth {
				continue
			}
			b.Run(fmt.Sprintf("depth=%d/limit=%d", depth, limit), func(b *testing.B) {
				svc := newBenchService(b)
				seedMessages(b, svc, "carol", depth)
				mb := svc.Client("carol")
				opts := store.ListOptions{Limit: limit}
				b.ReportAllocs()
				for b.Loop() {
					if _, err := mb.Folder(context.Background(), store.FolderInbox, opts); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkSearch measures full-text search using the store's native regex/FTS path.
func BenchmarkSearch(b *testing.B) {
	for _, depth := range []int{100, 1000} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			svc := newBenchService(b)
			seedMessages(b, svc, "dave", depth)
			mb := svc.Client("dave")
			q := SearchQuery{OwnerID: "dave", Query: "content"}
			b.ReportAllocs()
			for b.Loop() {
				res, err := mb.Search(context.Background(), q)
				if err != nil {
					b.Fatal(err)
				}
				benchSink = res
			}
		})
	}
}

// BenchmarkMarkRead measures single-message read marking.
func BenchmarkMarkRead(b *testing.B) {
	svc := newBenchService(b)
	ids := seedMessages(b, svc, "eve", 1000)
	mb := svc.Client("eve")
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		// Toggle read/unread so each call does real work.
		flags := MarkRead()
		if i%2 == 0 {
			flags = MarkUnread()
		}
		if err := mb.UpdateFlags(context.Background(), ids[i%len(ids)], flags); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// BenchmarkMarkAllRead measures bulk mark-all-read across different folder sizes.
func BenchmarkMarkAllRead(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			svc := newBenchService(b)
			seedMessages(b, svc, "frank", n)
			mb := svc.Client("frank")
			b.ReportAllocs()
			for b.Loop() {
				if _, err := mb.MarkAllRead(context.Background(), store.FolderInbox); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkStats measures per-user stats computation.
func BenchmarkStats(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			svc := newBenchService(b)
			seedMessages(b, svc, "grace", n)
			mb := svc.Client("grace")
			b.ReportAllocs()
			for b.Loop() {
				stats, err := mb.Stats(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				benchSink = stats
			}
		})
	}
}

// BenchmarkMoveToFolder measures single-message folder moves.
func BenchmarkMoveToFolder(b *testing.B) {
	svc := newBenchService(b)
	ids := seedMessages(b, svc, "henry", 1000)
	mb := svc.Client("henry")
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		// Alternate between archive and inbox to keep messages available.
		dst := store.FolderArchived
		if i%2 == 0 {
			dst = store.FolderInbox
		}
		if err := mb.MoveToFolder(context.Background(), ids[i%len(ids)], dst); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// BenchmarkDelete measures soft-delete followed by restore (so the pool stays full).
func BenchmarkDelete(b *testing.B) {
	svc := newBenchService(b)
	ids := seedMessages(b, svc, "ivy", 1000)
	mb := svc.Client("ivy")
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		// Pair each delete with a restore on the same message:
		// even i deletes ids[i/2], odd i restores it.
		id := ids[(i/2)%len(ids)]
		if i%2 == 0 {
			if err := mb.Delete(context.Background(), id); err != nil {
				b.Fatal(err)
			}
		} else {
			if err := mb.Restore(context.Background(), id); err != nil {
				b.Fatal(err)
			}
		}
		i++
	}
}

// BenchmarkMoveByFilter measures bulk move using filter-based operations.
func BenchmarkMoveByFilter(b *testing.B) {
	for _, n := range []int{10, 100} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			svc := newBenchService(b)
			seedMessages(b, svc, "jack", n)
			mb := svc.Client("jack")
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				src := store.FolderInbox
				dst := store.FolderArchived
				if i%2 == 0 {
					src, dst = dst, src
				}
				if _, err := mb.MoveByFilter(context.Background(),
					[]store.Filter{store.InFolder(src)}, dst); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}

// BenchmarkUpdateByFilter measures bulk mark-read by filter, contrasting the
// native BulkUpdater fast path (memory store implements MarkReadByFilter) with
// the paginated fallback (store wrapper that hides BulkUpdater). The two
// branches have very different cost: one UPDATE-like call vs N per-message
// MarkRead calls over a paginated Find.
func BenchmarkUpdateByFilter(b *testing.B) {
	const n = 100
	run := func(b *testing.B, svc *service) {
		seedMessages(b, svc, "owner", n)
		mb := svc.Client("owner")
		filters := []store.Filter{store.InFolder(store.FolderInbox)}
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			// Toggle read state so every iteration does real update work.
			flags := MarkRead()
			if i%2 == 0 {
				flags = MarkUnread()
			}
			if _, err := mb.UpdateByFilter(context.Background(), filters, flags); err != nil {
				b.Fatal(err)
			}
			i++
		}
	}
	b.Run("fast-path", func(b *testing.B) { run(b, newBenchService(b)) })
	b.Run("fallback", func(b *testing.B) { run(b, newNoBulkBenchService(b)) })
}

// BenchmarkDeleteByFilter measures bulk soft-delete by filter, contrasting the
// native BulkUpdater fast path with the paginated fallback. Each iteration
// pairs a delete (inbox -> trash) with a restore (trash -> inbox) so the
// working set stays full; both halves go through the same store, so the
// fast-path vs fallback comparison is apples-to-apples.
func BenchmarkDeleteByFilter(b *testing.B) {
	const n = 100
	run := func(b *testing.B, svc *service) {
		seedMessages(b, svc, "owner", n)
		mb := svc.Client("owner")
		inbox := []store.Filter{store.InFolder(store.FolderInbox)}
		trash := []store.Filter{store.InFolder(store.FolderTrash)}
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			if i%2 == 0 {
				if _, err := mb.DeleteByFilter(context.Background(), inbox); err != nil {
					b.Fatal(err)
				}
			} else {
				// Restore the just-trashed messages back to the inbox.
				if _, err := mb.MoveByFilter(context.Background(), trash, store.FolderInbox); err != nil {
					b.Fatal(err)
				}
			}
			i++
		}
	}
	b.Run("fast-path", func(b *testing.B) { run(b, newBenchService(b)) })
	b.Run("fallback", func(b *testing.B) { run(b, newNoBulkBenchService(b)) })
}

// BenchmarkSendMessage_BodySize measures single-message delivery as the body
// grows, isolating the cost the message body contributes to the send path
// (validation, copy, store write) from the fixed per-send overhead.
func BenchmarkSendMessage_BodySize(b *testing.B) {
	for _, bs := range []struct {
		name string
		size int
	}{
		{"1KB", 1024},
		{"100KB", 100 * 1024},
	} {
		b.Run(bs.name, func(b *testing.B) {
			svc := newBenchService(b)
			mb := svc.Client("alice")
			body := strings.Repeat("x", bs.size)
			b.ReportAllocs()
			for b.Loop() {
				msg, err := mb.SendMessage(context.Background(), SendRequest{
					RecipientIDs: []string{"alice"},
					Subject:      "Sized",
					Body:         body,
				})
				if err != nil {
					b.Fatal(err)
				}
				benchSink = msg
			}
		})
	}
}

// BenchmarkSendMessage_Parallel measures throughput under concurrent senders.
func BenchmarkSendMessage_Parallel(b *testing.B) {
	svc := newBenchService(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		mb := svc.Client("shared")
		for pb.Next() {
			if _, err := mb.SendMessage(context.Background(), SendRequest{
				RecipientIDs: []string{"shared"},
				Subject:      "Parallel",
				Body:         "Concurrent message",
			}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkGet_Parallel measures concurrent read throughput.
func BenchmarkGet_Parallel(b *testing.B) {
	svc := newBenchService(b)
	ids := seedMessages(b, svc, "kate", 500)
	b.ReportAllocs()
	var counter atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		mb := svc.Client("kate")
		for pb.Next() {
			idx := int(counter.Add(1)) % len(ids)
			if _, err := mb.Get(context.Background(), ids[idx]); err != nil {
				b.Fatal(err)
			}
		}
	})
}
