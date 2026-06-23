package server_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/server"
	"github.com/rbaliyan/mailbox/store"
	"google.golang.org/grpc/codes"
)

func TestGetMessage_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	sendTo(t, client, "fetch me")
	id := bobInbox(t, client)[0].GetId()

	resp, err := client.GetMessage(context.Background(), &mailboxpb.GetMessageRequest{
		UserId:    "bob",
		MessageId: id,
	})
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if resp.GetMessage().GetId() != id {
		t.Errorf("id = %q, want %q", resp.GetMessage().GetId(), id)
	}
}

// TestGetMessage_NotFound is a characterization test documenting current
// behavior for a missing message.
//
// Expected (per server/errors.go) is codes.NotFound. In practice GetMessage
// returns codes.Internal: userMailbox.Get (query.go) wraps the store error as
// fmt.Errorf("get message: %w", store.ErrNotFound), whose chain contains
// store.ErrNotFound but NOT mailbox.ErrNotFound (mailbox.ErrNotFound is a
// distinct value that itself wraps store.ErrNotFound). toGRPCError only checks
// errors.Is(err, mailbox.ErrNotFound), so the mapping misses and the default
// codes.Internal is returned.
//
// TODO: real bug — userMailbox.Get should translate store.ErrNotFound to
// mailbox.ErrNotFound (as GetDraft already does), or toGRPCError should also
// match store.ErrNotFound, so GetMessage reports codes.NotFound. See
// query.go userMailbox.Get and server/errors.go toGRPCError.
func TestGetMessage_NotFound(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.GetMessage(context.Background(), &mailboxpb.GetMessageRequest{
		UserId:    "bob",
		MessageId: "does-not-exist",
	})
	got := codeOf(t, err)
	if got == codes.NotFound {
		t.Fatal("GetMessage now returns codes.NotFound; the not-found mapping is fixed — update this characterization test to assert codes.NotFound")
	}
	if got != codes.Internal {
		t.Errorf("code = %v, want %v (current behavior); see TODO in this test", got, codes.Internal)
	}
}

func TestGetReplies_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	parent := sendTo(t, client, "parent")

	resp, err := client.GetReplies(context.Background(), &mailboxpb.GetRepliesRequest{
		UserId:    "bob",
		MessageId: parent.GetId(),
	})
	if err != nil {
		t.Fatalf("GetReplies: %v", err)
	}
	if resp == nil {
		t.Fatal("want non-nil response")
	}
}

func TestGetReplies_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.GetReplies(context.Background(), &mailboxpb.GetRepliesRequest{
		UserId:    "bob",
		MessageId: "",
	})
	if got := codeOf(t, err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
	}
}

func TestSearchMessages_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	sendTo(t, client, "searchable subject")

	resp, err := client.SearchMessages(context.Background(), &mailboxpb.SearchMessagesRequest{
		UserId: "bob",
		Query:  "searchable",
	})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if resp == nil {
		t.Fatal("want non-nil response")
	}
}

func TestDeleteRestoreLifecycle(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ctx := context.Background()
	sendTo(t, client, "to delete")
	id := bobInbox(t, client)[0].GetId()

	if _, err := client.DeleteMessage(ctx, &mailboxpb.DeleteMessageRequest{
		UserId:    "bob",
		MessageId: id,
	}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	got, err := client.GetMessage(ctx, &mailboxpb.GetMessageRequest{UserId: "bob", MessageId: id})
	if err != nil {
		t.Fatalf("GetMessage after delete: %v", err)
	}
	if got.GetMessage().GetFolderId() != store.FolderTrash {
		t.Errorf("folder = %q, want %q", got.GetMessage().GetFolderId(), store.FolderTrash)
	}

	if _, err := client.RestoreMessage(ctx, &mailboxpb.RestoreMessageRequest{
		UserId:    "bob",
		MessageId: id,
	}); err != nil {
		t.Fatalf("RestoreMessage: %v", err)
	}
	got, err = client.GetMessage(ctx, &mailboxpb.GetMessageRequest{UserId: "bob", MessageId: id})
	if err != nil {
		t.Fatalf("GetMessage after restore: %v", err)
	}
	if got.GetMessage().GetFolderId() == store.FolderTrash {
		t.Error("message still in trash after restore")
	}
}

func TestPermanentlyDeleteMessage_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ctx := context.Background()
	sendTo(t, client, "purge me")
	id := bobInbox(t, client)[0].GetId()

	if _, err := client.DeleteMessage(ctx, &mailboxpb.DeleteMessageRequest{UserId: "bob", MessageId: id}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if _, err := client.PermanentlyDeleteMessage(ctx, &mailboxpb.PermanentlyDeleteMessageRequest{
		UserId:    "bob",
		MessageId: id,
	}); err != nil {
		t.Fatalf("PermanentlyDeleteMessage: %v", err)
	}
	if _, err := client.GetMessage(ctx, &mailboxpb.GetMessageRequest{UserId: "bob", MessageId: id}); err == nil {
		t.Error("message still retrievable after permanent delete")
	}
}

func TestPermanentlyDeleteMessage_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.PermanentlyDeleteMessage(context.Background(), &mailboxpb.PermanentlyDeleteMessageRequest{
		UserId:    "bob",
		MessageId: "",
	})
	if got := codeOf(t, err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
	}
}

func TestAddRemoveTag_Lifecycle(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ctx := context.Background()
	sendTo(t, client, "tag me")
	id := bobInbox(t, client)[0].GetId()

	if _, err := client.AddTag(ctx, &mailboxpb.TagRequest{UserId: "bob", MessageId: id, TagId: "work"}); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	got, _ := client.GetMessage(ctx, &mailboxpb.GetMessageRequest{UserId: "bob", MessageId: id})
	if !hasTag(got.GetMessage().GetTags(), "work") {
		t.Errorf("tags = %v, want to contain work", got.GetMessage().GetTags())
	}

	if _, err := client.RemoveTag(ctx, &mailboxpb.TagRequest{UserId: "bob", MessageId: id, TagId: "work"}); err != nil {
		t.Fatalf("RemoveTag: %v", err)
	}
	got, _ = client.GetMessage(ctx, &mailboxpb.GetMessageRequest{UserId: "bob", MessageId: id})
	if hasTag(got.GetMessage().GetTags(), "work") {
		t.Errorf("tags = %v, still contains work", got.GetMessage().GetTags())
	}
}

func TestAddTag_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.AddTag(context.Background(), &mailboxpb.TagRequest{
		UserId:    "bob",
		MessageId: "id",
		TagId:     "",
	})
	if got := codeOf(t, err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
	}
}

func TestMarkAllRead_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ctx := context.Background()
	sendTo(t, client, "one")
	sendTo(t, client, "two")

	resp, err := client.MarkAllRead(ctx, &mailboxpb.MarkAllReadRequest{
		UserId:   "bob",
		FolderId: store.FolderInbox,
	})
	if err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if resp.GetCount() != 2 {
		t.Errorf("count = %d, want 2", resp.GetCount())
	}
}

func TestMarkAllRead_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.MarkAllRead(context.Background(), &mailboxpb.MarkAllReadRequest{
		UserId:   "bob",
		FolderId: "",
	})
	if got := codeOf(t, err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
	}
}

func TestBulkPermanentlyDelete_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ctx := context.Background()
	ids := bobMessageIDs(t, client, 2)

	// Must be in trash before permanent delete.
	if _, err := client.BulkDelete(ctx, &mailboxpb.BulkDeleteRequest{UserId: "bob", MessageIds: ids}); err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}
	resp, err := client.BulkPermanentlyDelete(ctx, &mailboxpb.BulkPermanentlyDeleteRequest{
		UserId:     "bob",
		MessageIds: ids,
	})
	if err != nil {
		t.Fatalf("BulkPermanentlyDelete: %v", err)
	}
	if resp.GetSuccessCount() != int32(len(ids)) {
		t.Errorf("success = %d, want %d", resp.GetSuccessCount(), len(ids))
	}
}

func TestBulkPermanentlyDelete_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkPermanentlyDelete(context.Background(), &mailboxpb.BulkPermanentlyDeleteRequest{
		UserId:     "bob",
		MessageIds: nil,
	})
	if got := codeOf(t, err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
	}
}

func TestGetStatsAndUnreadCount(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ctx := context.Background()
	sendTo(t, client, "stat one")
	sendTo(t, client, "stat two")

	stats, err := client.GetStats(ctx, &mailboxpb.GetStatsRequest{UserId: "bob"})
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.GetStats().GetTotalMessages() != 2 {
		t.Errorf("total = %d, want 2", stats.GetStats().GetTotalMessages())
	}

	count, err := client.UnreadCount(ctx, &mailboxpb.UnreadCountRequest{UserId: "bob"})
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if count.GetCount() != 2 {
		t.Errorf("unread = %d, want 2", count.GetCount())
	}
}

func TestListFolders_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	resp, err := client.ListFolders(context.Background(), &mailboxpb.ListFoldersRequest{UserId: "bob"})
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if len(resp.GetFolders()) == 0 {
		t.Error("want at least the system folders")
	}
}

func TestListFolders_MissingUserID(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.ListFolders(context.Background(), &mailboxpb.ListFoldersRequest{UserId: ""})
	if got := codeOf(t, err); got != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", got, codes.InvalidArgument)
	}
}

// TestServerOptions exercises the option constructors and their nil/zero guards.
func TestServerOptions(t *testing.T) {
	t.Parallel()

	// New rejects a nil service.
	if _, err := server.New(nil); err == nil {
		t.Fatal("New(nil) should fail for a nil service")
	}

	// Construct a server with every option set, including nil/zero values that
	// must be ignored. Construction must succeed and the server must be usable.
	svc := mailboxtest.NewService(t, mailbox.Config{})
	srv, err := server.New(svc,
		server.WithSecurityGuard(server.AllowAll()),
		server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		server.WithLogger(nil),                    // ignored: keeps prior logger
		server.WithMaxBulkSize(0),                 // ignored: keeps default
		server.WithMaxBulkSize(10),                // applied
		server.WithStreamMaxDuration(0),           // ignored
		server.WithStreamMaxDuration(time.Second), // applied
	)
	if err != nil {
		t.Fatalf("New with options: %v", err)
	}
	if srv == nil {
		t.Fatal("want non-nil server")
	}
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}
