package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/rbaliyan/mailbox"
	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/store"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestConvert_ToProtoStats(t *testing.T) {
	t.Parallel()

	// nil input yields a non-nil empty proto.
	if got := toProtoStats(nil); got == nil {
		t.Fatal("toProtoStats(nil): want non-nil, got nil")
	}

	in := &store.MailboxStats{
		TotalMessages: 10,
		UnreadCount:   3,
		DraftCount:    2,
		Folders: map[string]store.FolderCounts{
			store.FolderInbox: {Total: 7, Unread: 3},
		},
	}
	got := toProtoStats(in)
	if got.GetTotalMessages() != 10 {
		t.Errorf("total: want 10, got %d", got.GetTotalMessages())
	}
	if got.GetUnreadCount() != 3 {
		t.Errorf("unread: want 3, got %d", got.GetUnreadCount())
	}
	if got.GetDraftCount() != 2 {
		t.Errorf("draft: want 2, got %d", got.GetDraftCount())
	}
	fc, ok := got.GetFolders()[store.FolderInbox]
	if !ok {
		t.Fatalf("folder %q missing in %v", store.FolderInbox, got.GetFolders())
	}
	if fc.GetTotal() != 7 || fc.GetUnread() != 3 {
		t.Errorf("folder counts: want {7,3}, got {%d,%d}", fc.GetTotal(), fc.GetUnread())
	}
}

func TestConvert_ToProtoFolderInfo(t *testing.T) {
	t.Parallel()

	in := mailbox.FolderInfo{
		ID:           "__inbox",
		Name:         "Inbox",
		IsSystem:     true,
		MessageCount: 42,
		UnreadCount:  5,
	}
	got := toProtoFolderInfo(in)
	if got.GetId() != in.ID {
		t.Errorf("id: want %q, got %q", in.ID, got.GetId())
	}
	if got.GetName() != in.Name {
		t.Errorf("name: want %q, got %q", in.Name, got.GetName())
	}
	if got.GetIsSystem() != in.IsSystem {
		t.Errorf("is_system: want %v, got %v", in.IsSystem, got.GetIsSystem())
	}
	if got.GetMessageCount() != in.MessageCount {
		t.Errorf("message_count: want %d, got %d", in.MessageCount, got.GetMessageCount())
	}
	if got.GetUnreadCount() != in.UnreadCount {
		t.Errorf("unread_count: want %d, got %d", in.UnreadCount, got.GetUnreadCount())
	}
}

func TestConvert_ToProtoAttachment_Nil(t *testing.T) {
	t.Parallel()
	if got := toProtoAttachment(nil); got != nil {
		t.Errorf("toProtoAttachment(nil): want nil, got %v", got)
	}
}

func TestConvert_ToProtoBulkResult(t *testing.T) {
	t.Parallel()

	// nil yields an empty response.
	if got := toProtoBulkResult(nil); got.GetSuccessCount() != 0 || got.GetFailureCount() != 0 {
		t.Errorf("toProtoBulkResult(nil): want {0,0}, got {%d,%d}",
			got.GetSuccessCount(), got.GetFailureCount())
	}

	in := &mailbox.BulkResult{
		Results: []mailbox.OperationResult{
			{ID: "a", Success: true},
			{ID: "b", Error: mailbox.ErrNotFound},
		},
	}
	got := toProtoBulkResult(in)
	if got.GetSuccessCount() != 1 {
		t.Errorf("success: want 1, got %d", got.GetSuccessCount())
	}
	if got.GetFailureCount() != 1 {
		t.Errorf("failure: want 1, got %d", got.GetFailureCount())
	}
	if len(got.GetResults()) != 2 {
		t.Fatalf("results: want 2, got %d", len(got.GetResults()))
	}
	if r := got.GetResults()[0]; r.GetId() != "a" || !r.GetSuccess() || r.GetError() != "" {
		t.Errorf("result[0]: want {a,true,\"\"}, got {%s,%v,%q}", r.GetId(), r.GetSuccess(), r.GetError())
	}
	if r := got.GetResults()[1]; r.GetId() != "b" || r.GetSuccess() || r.GetError() != "message not found" {
		t.Errorf("result[1]: want {b,false,\"message not found\"}, got {%s,%v,%q}",
			r.GetId(), r.GetSuccess(), r.GetError())
	}
}

func boolPtrValue(t *testing.T, p *bool) string {
	t.Helper()
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%v", *p)
}

func TestConvert_FromProtoFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		in           *mailboxpb.Flags
		wantRead     string // "nil", "true", or "false"
		wantArchived string
	}{
		{name: "nil", in: nil, wantRead: "nil", wantArchived: "nil"},
		{
			name:         "read true only",
			in:           &mailboxpb.Flags{Read: wrapperspb.Bool(true)},
			wantRead:     "true",
			wantArchived: "nil",
		},
		{
			name:         "archived false only",
			in:           &mailboxpb.Flags{Archived: wrapperspb.Bool(false)},
			wantRead:     "nil",
			wantArchived: "false",
		},
		{
			name:         "both set",
			in:           &mailboxpb.Flags{Read: wrapperspb.Bool(false), Archived: wrapperspb.Bool(true)},
			wantRead:     "false",
			wantArchived: "true",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fromProtoFlags(tc.in)
			if r := boolPtrValue(t, got.Read); r != tc.wantRead {
				t.Errorf("Read: want %s, got %s", tc.wantRead, r)
			}
			if a := boolPtrValue(t, got.Archived); a != tc.wantArchived {
				t.Errorf("Archived: want %s, got %s", tc.wantArchived, a)
			}
		})
	}
}

func TestConvert_FromProtoListOptions(t *testing.T) {
	t.Parallel()

	// nil yields the zero ListOptions.
	if got := fromProtoListOptions(nil); got != (store.ListOptions{}) {
		t.Errorf("fromProtoListOptions(nil): want zero, got %+v", got)
	}

	tests := []struct {
		name      string
		order     string
		wantOrder store.SortOrder
	}{
		{name: "desc", order: "desc", wantOrder: store.SortDesc},
		{name: "asc", order: "asc", wantOrder: store.SortAsc},
		{name: "other", order: "weird", wantOrder: store.SortAsc},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := &mailboxpb.ListOptions{
				Limit:      25,
				Offset:     5,
				SortBy:     "created_at",
				SortOrder:  tc.order,
				StartAfter: "cursor-1",
			}
			got := fromProtoListOptions(in)
			if got.Limit != 25 {
				t.Errorf("limit: want 25, got %d", got.Limit)
			}
			if got.Offset != 5 {
				t.Errorf("offset: want 5, got %d", got.Offset)
			}
			if got.SortBy != "created_at" {
				t.Errorf("sort_by: want %q, got %q", "created_at", got.SortBy)
			}
			if got.StartAfter != "cursor-1" {
				t.Errorf("start_after: want %q, got %q", "cursor-1", got.StartAfter)
			}
			if got.SortOrder != tc.wantOrder {
				t.Errorf("sort_order: want %v, got %v", tc.wantOrder, got.SortOrder)
			}
		})
	}
}

func TestConvert_SafeErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "not found", err: mailbox.ErrNotFound, want: "message not found"},
		{name: "arbitrary", err: fmt.Errorf("boom"), want: "internal error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeErrorMessage(tc.err); got != tc.want {
				t.Errorf("safeErrorMessage: want %q, got %q", tc.want, got)
			}
		})
	}
}

// TestConvert_ToProtoMessage exercises the *Server.toProtoMessage and
// toProtoMessageList converters using a real message sent through the mailbox.
func TestConvert_ToProtoMessage(t *testing.T) {
	t.Parallel()

	svc := mailboxtest.NewService(t, mailbox.Config{})
	srv, err := New(svc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	sent := mailboxtest.SendMessage(t, svc.Client("alice"), "bob", "Subject", "Body")
	if sent == nil {
		t.Fatal("want sent message")
	}

	// nil converts to nil.
	if got := srv.toProtoMessage(nil); got != nil {
		t.Errorf("toProtoMessage(nil): want nil, got %v", got)
	}

	// Fetch bob's delivered copy and convert it.
	inbox := mailboxtest.Inbox(t, svc.Client("bob"))
	if len(inbox) < 1 {
		t.Fatal("want at least 1 inbox message")
	}
	msg := inbox[0]
	pb := srv.toProtoMessage(msg)
	if pb == nil {
		t.Fatal("toProtoMessage: want non-nil")
	}
	if pb.GetId() != msg.GetID() {
		t.Errorf("id: want %q, got %q", msg.GetID(), pb.GetId())
	}
	if pb.GetSubject() != msg.GetSubject() {
		t.Errorf("subject: want %q, got %q", msg.GetSubject(), pb.GetSubject())
	}
	if pb.GetOwnerId() != msg.GetOwnerID() {
		t.Errorf("owner: want %q, got %q", msg.GetOwnerID(), pb.GetOwnerId())
	}

	// toProtoMessageList over bob's inbox folder.
	list, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Folder: %v", err)
	}
	resp := srv.toProtoMessageList(list)
	if resp == nil {
		t.Fatal("toProtoMessageList: want non-nil")
	}
	if len(resp.GetMessages()) != len(list.All()) {
		t.Errorf("message count: want %d, got %d", len(list.All()), len(resp.GetMessages()))
	}
}
