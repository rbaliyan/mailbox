package server_test

import (
	"context"
	"testing"

	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/server"
	"github.com/rbaliyan/mailbox/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// sendTo sends a message from alice to bob via the SendMessage RPC and returns
// the created message. It fails the test on any error.
func sendTo(t *testing.T, client mailboxpb.MailboxServiceClient, subject string) *mailboxpb.Message {
	t.Helper()
	resp, err := client.SendMessage(context.Background(), &mailboxpb.SendMessageRequest{
		UserId:       "alice",
		RecipientIds: []string{"bob"},
		Subject:      subject,
		Body:         "body",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	return resp.GetMessage()
}

// bobInbox lists bob's inbox and returns the messages.
func bobInbox(t *testing.T, client mailboxpb.MailboxServiceClient) []*mailboxpb.Message {
	t.Helper()
	resp, err := client.ListMessages(context.Background(), &mailboxpb.ListMessagesRequest{
		UserId:   "bob",
		FolderId: store.FolderInbox,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	return resp.GetMessages()
}

// codeOf returns the gRPC status code of err, failing if err is not a status.
func codeOf(t *testing.T, err error) codes.Code {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("want gRPC status error, got %T: %v", err, err)
	}
	return st.Code()
}

// bobMessageIDs sends n messages to bob and returns their inbox message IDs.
func bobMessageIDs(t *testing.T, client mailboxpb.MailboxServiceClient, n int) []string {
	t.Helper()
	for i := 0; i < n; i++ {
		sendTo(t, client, "bulk")
	}
	msgs := bobInbox(t, client)
	if len(msgs) < n {
		t.Fatalf("want at least %d inbox messages, got %d", n, len(msgs))
	}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, msgs[i].GetId())
	}
	return ids
}

func TestSendMessage_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	resp, err := client.SendMessage(context.Background(), &mailboxpb.SendMessageRequest{
		UserId:       "alice",
		RecipientIds: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp.GetMessage().GetId() == "" {
		t.Error("want non-empty message ID")
	}
	if got := resp.GetMessage().GetSubject(); got != "Hello" {
		t.Errorf("subject: want %q, got %q", "Hello", got)
	}
}

func TestSendMessage_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.SendMessage(context.Background(), &mailboxpb.SendMessageRequest{
		UserId:       "alice",
		RecipientIds: nil,
		Subject:      "",
		Body:         "",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestListMessages_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	sendTo(t, client, "Hello")

	resp, err := client.ListMessages(context.Background(), &mailboxpb.ListMessagesRequest{
		UserId:   "bob",
		FolderId: store.FolderInbox,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(resp.GetMessages()) < 1 {
		t.Errorf("want at least 1 message, got %d", len(resp.GetMessages()))
	}
}

func TestListMessages_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.ListMessages(context.Background(), &mailboxpb.ListMessagesRequest{
		UserId:   "bob",
		FolderId: "",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestGetThread_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	msg := sendTo(t, client, "Hello")

	// A freshly sent message anchors a thread; use its thread id, falling back
	// to the message id when the thread id is not populated.
	threadID := msg.GetThreadId()
	if threadID == "" {
		threadID = msg.GetId()
	}

	resp, err := client.GetThread(context.Background(), &mailboxpb.GetThreadRequest{
		UserId:   "alice",
		ThreadId: threadID,
	})
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if resp == nil {
		t.Fatal("want non-nil response")
	}
}

func TestGetThread_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.GetThread(context.Background(), &mailboxpb.GetThreadRequest{
		UserId:   "alice",
		ThreadId: "",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestUpdateFlags_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	sendTo(t, client, "Hello")

	msgs := bobInbox(t, client)
	if len(msgs) < 1 {
		t.Fatal("want at least 1 inbox message")
	}
	id := msgs[0].GetId()

	_, err := client.UpdateFlags(context.Background(), &mailboxpb.UpdateFlagsRequest{
		UserId:    "bob",
		MessageId: id,
		Flags:     &mailboxpb.Flags{Read: wrapperspb.Bool(true)},
	})
	if err != nil {
		t.Fatalf("UpdateFlags: %v", err)
	}

	getResp, err := client.GetMessage(context.Background(), &mailboxpb.GetMessageRequest{
		UserId:    "bob",
		MessageId: id,
	})
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !getResp.GetMessage().GetIsRead() {
		t.Error("want message marked read")
	}
}

func TestUpdateFlags_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.UpdateFlags(context.Background(), &mailboxpb.UpdateFlagsRequest{
		UserId:    "bob",
		MessageId: "",
		Flags:     &mailboxpb.Flags{Read: wrapperspb.Bool(true)},
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestMoveMessage_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	sendTo(t, client, "Hello")

	msgs := bobInbox(t, client)
	if len(msgs) < 1 {
		t.Fatal("want at least 1 inbox message")
	}
	id := msgs[0].GetId()

	_, err := client.MoveMessage(context.Background(), &mailboxpb.MoveMessageRequest{
		UserId:    "bob",
		MessageId: id,
		FolderId:  store.FolderArchived,
	})
	if err != nil {
		t.Fatalf("MoveMessage: %v", err)
	}

	getResp, err := client.GetMessage(context.Background(), &mailboxpb.GetMessageRequest{
		UserId:    "bob",
		MessageId: id,
	})
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got := getResp.GetMessage().GetFolderId(); got != store.FolderArchived {
		t.Errorf("folder: want %q, got %q", store.FolderArchived, got)
	}
}

func TestMoveMessage_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.MoveMessage(context.Background(), &mailboxpb.MoveMessageRequest{
		UserId:    "bob",
		MessageId: "msg-1",
		FolderId:  "",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestBulkUpdateFlags_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ids := bobMessageIDs(t, client, 2)

	resp, err := client.BulkUpdateFlags(context.Background(), &mailboxpb.BulkUpdateFlagsRequest{
		UserId:     "bob",
		MessageIds: ids,
		Flags:      &mailboxpb.Flags{Read: wrapperspb.Bool(true)},
	})
	if err != nil {
		t.Fatalf("BulkUpdateFlags: %v", err)
	}
	if int(resp.GetSuccessCount()) != len(ids) {
		t.Errorf("success: want %d, got %d", len(ids), resp.GetSuccessCount())
	}
	if resp.GetFailureCount() != 0 {
		t.Errorf("failure: want 0, got %d", resp.GetFailureCount())
	}
}

func TestBulkUpdateFlags_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkUpdateFlags(context.Background(), &mailboxpb.BulkUpdateFlagsRequest{
		UserId:     "bob",
		MessageIds: nil,
		Flags:      &mailboxpb.Flags{Read: wrapperspb.Bool(true)},
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestBulkMove_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ids := bobMessageIDs(t, client, 2)

	resp, err := client.BulkMove(context.Background(), &mailboxpb.BulkMoveRequest{
		UserId:     "bob",
		MessageIds: ids,
		FolderId:   store.FolderArchived,
	})
	if err != nil {
		t.Fatalf("BulkMove: %v", err)
	}
	if int(resp.GetSuccessCount()) != len(ids) {
		t.Errorf("success: want %d, got %d", len(ids), resp.GetSuccessCount())
	}
	if resp.GetFailureCount() != 0 {
		t.Errorf("failure: want 0, got %d", resp.GetFailureCount())
	}
}

func TestBulkMove_EmptyIDs_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkMove(context.Background(), &mailboxpb.BulkMoveRequest{
		UserId:     "bob",
		MessageIds: nil,
		FolderId:   store.FolderArchived,
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestBulkMove_MissingFolder_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkMove(context.Background(), &mailboxpb.BulkMoveRequest{
		UserId:     "bob",
		MessageIds: []string{"msg-1"},
		FolderId:   "",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestBulkDelete_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ids := bobMessageIDs(t, client, 2)

	resp, err := client.BulkDelete(context.Background(), &mailboxpb.BulkDeleteRequest{
		UserId:     "bob",
		MessageIds: ids,
	})
	if err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}
	if int(resp.GetSuccessCount()) != len(ids) {
		t.Errorf("success: want %d, got %d", len(ids), resp.GetSuccessCount())
	}
	if resp.GetFailureCount() != 0 {
		t.Errorf("failure: want 0, got %d", resp.GetFailureCount())
	}
}

func TestBulkDelete_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkDelete(context.Background(), &mailboxpb.BulkDeleteRequest{
		UserId:     "bob",
		MessageIds: nil,
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestBulkAddTag_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ids := bobMessageIDs(t, client, 2)

	resp, err := client.BulkAddTag(context.Background(), &mailboxpb.BulkTagRequest{
		UserId:     "bob",
		MessageIds: ids,
		TagId:      "important",
	})
	if err != nil {
		t.Fatalf("BulkAddTag: %v", err)
	}
	if int(resp.GetSuccessCount()) != len(ids) {
		t.Errorf("success: want %d, got %d", len(ids), resp.GetSuccessCount())
	}
	if resp.GetFailureCount() != 0 {
		t.Errorf("failure: want 0, got %d", resp.GetFailureCount())
	}
}

func TestBulkAddTag_EmptyIDs_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkAddTag(context.Background(), &mailboxpb.BulkTagRequest{
		UserId:     "bob",
		MessageIds: nil,
		TagId:      "important",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestBulkAddTag_MissingTag_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkAddTag(context.Background(), &mailboxpb.BulkTagRequest{
		UserId:     "bob",
		MessageIds: []string{"msg-1"},
		TagId:      "",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestBulkRemoveTag_Happy(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())
	ids := bobMessageIDs(t, client, 2)

	if _, err := client.BulkAddTag(context.Background(), &mailboxpb.BulkTagRequest{
		UserId:     "bob",
		MessageIds: ids,
		TagId:      "important",
	}); err != nil {
		t.Fatalf("BulkAddTag (setup): %v", err)
	}

	resp, err := client.BulkRemoveTag(context.Background(), &mailboxpb.BulkTagRequest{
		UserId:     "bob",
		MessageIds: ids,
		TagId:      "important",
	})
	if err != nil {
		t.Fatalf("BulkRemoveTag: %v", err)
	}
	if int(resp.GetSuccessCount()) != len(ids) {
		t.Errorf("success: want %d, got %d", len(ids), resp.GetSuccessCount())
	}
	if resp.GetFailureCount() != 0 {
		t.Errorf("failure: want 0, got %d", resp.GetFailureCount())
	}
}

func TestBulkRemoveTag_EmptyIDs_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkRemoveTag(context.Background(), &mailboxpb.BulkTagRequest{
		UserId:     "bob",
		MessageIds: nil,
		TagId:      "important",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

func TestBulkRemoveTag_MissingTag_InvalidArgument(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.AllowAll())

	_, err := client.BulkRemoveTag(context.Background(), &mailboxpb.BulkTagRequest{
		UserId:     "bob",
		MessageIds: []string{"msg-1"},
		TagId:      "",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.InvalidArgument {
		t.Errorf("code: want %v, got %v", codes.InvalidArgument, c)
	}
}

// TestSendMessage_DenyAll verifies that a write RPC is rejected when the
// DenyAll guard is installed. Matching the existing DenyAll behavior, the
// interceptor yields codes.Unauthenticated.
func TestSendMessage_DenyAll(t *testing.T) {
	t.Parallel()
	client := startServer(t, server.DenyAll())

	_, err := client.SendMessage(context.Background(), &mailboxpb.SendMessageRequest{
		UserId:       "alice",
		RecipientIds: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if c := codeOf(t, err); c != codes.Unauthenticated && c != codes.PermissionDenied {
		t.Errorf("code: want %v or %v, got %v", codes.Unauthenticated, codes.PermissionDenied, c)
	}
}
