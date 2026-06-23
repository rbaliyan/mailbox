package server_test

import (
	"context"
	"testing"
	"time"

	mailboxpb "github.com/rbaliyan/mailbox/proto/mailbox/v1"
	"github.com/rbaliyan/mailbox/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// denyAuthorizeGuard authenticates successfully but denies every authorization,
// driving the PermissionDenied branch of Server.authorize across handlers.
type denyAuthorizeGuard struct{}

func (denyAuthorizeGuard) Authenticate(context.Context) (server.Identity, error) {
	return denyAuthorizeGuard{}, nil
}
func (denyAuthorizeGuard) Authorize(context.Context, server.Identity, server.Action, server.Resource) (server.Decision, error) {
	return server.Decision{Allowed: false, Reason: "denied by policy"}, nil
}
func (denyAuthorizeGuard) UserID() string         { return "denied-user" }
func (denyAuthorizeGuard) Claims() map[string]any { return map[string]any{"role": "none"} }

// TestServer_AuthorizeDenied drives the authorize-denied path of each handler.
func TestServer_AuthorizeDenied(t *testing.T) {
	client := startServer(t, denyAuthorizeGuard{})
	ctx := context.Background()
	const id = "m1"

	type call struct {
		name string
		fn   func() error
	}
	calls := []call{
		{"GetMessage", func() error {
			_, err := client.GetMessage(ctx, &mailboxpb.GetMessageRequest{UserId: "alice", MessageId: id})
			return err
		}},
		{"SendMessage", func() error {
			_, err := client.SendMessage(ctx, &mailboxpb.SendMessageRequest{
				UserId: "alice", RecipientIds: []string{"bob"}, Subject: "s", Body: "b",
			})
			return err
		}},
		{"DeleteMessage", func() error {
			_, err := client.DeleteMessage(ctx, &mailboxpb.DeleteMessageRequest{UserId: "alice", MessageId: id})
			return err
		}},
		{"RestoreMessage", func() error {
			_, err := client.RestoreMessage(ctx, &mailboxpb.RestoreMessageRequest{UserId: "alice", MessageId: id})
			return err
		}},
		{"PermanentlyDeleteMessage", func() error {
			_, err := client.PermanentlyDeleteMessage(ctx, &mailboxpb.PermanentlyDeleteMessageRequest{UserId: "alice", MessageId: id})
			return err
		}},
		{"AddTag", func() error {
			_, err := client.AddTag(ctx, &mailboxpb.TagRequest{UserId: "alice", MessageId: id, TagId: "t"})
			return err
		}},
		{"RemoveTag", func() error {
			_, err := client.RemoveTag(ctx, &mailboxpb.TagRequest{UserId: "alice", MessageId: id, TagId: "t"})
			return err
		}},
		{"ComposeDraft", func() error {
			_, err := client.ComposeDraft(ctx, &mailboxpb.ComposeDraftRequest{UserId: "alice", Subject: "s"})
			return err
		}},
		{"DeleteDraft", func() error {
			_, err := client.DeleteDraft(ctx, &mailboxpb.DeleteDraftRequest{UserId: "alice", DraftId: "d"})
			return err
		}},
		{"GetStats", func() error {
			_, err := client.GetStats(ctx, &mailboxpb.GetStatsRequest{UserId: "alice"})
			return err
		}},
		{"UnreadCount", func() error {
			_, err := client.UnreadCount(ctx, &mailboxpb.UnreadCountRequest{UserId: "alice"})
			return err
		}},
		{"MarkAllRead", func() error {
			_, err := client.MarkAllRead(ctx, &mailboxpb.MarkAllReadRequest{UserId: "alice", FolderId: "__inbox"})
			return err
		}},
	}

	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			err := c.fn()
			if err == nil {
				t.Fatalf("%s: expected PermissionDenied, got nil", c.name)
			}
			if code := status.Code(err); code != codes.PermissionDenied {
				t.Errorf("%s: code = %v, want PermissionDenied", c.name, code)
			}
		})
	}
}

// TestServer_FieldValidation drives the required-field validation branches.
func TestServer_FieldValidation(t *testing.T) {
	client := startServer(t, server.AllowAll())
	ctx := context.Background()

	type tc struct {
		name string
		fn   func() error
	}
	cases := []tc{
		{"DeleteMessage missing id", func() error {
			_, err := client.DeleteMessage(ctx, &mailboxpb.DeleteMessageRequest{UserId: "alice"})
			return err
		}},
		{"AddTag missing tag", func() error {
			_, err := client.AddTag(ctx, &mailboxpb.TagRequest{UserId: "alice", MessageId: "m"})
			return err
		}},
		{"RemoveTag missing message", func() error {
			_, err := client.RemoveTag(ctx, &mailboxpb.TagRequest{UserId: "alice", TagId: "t"})
			return err
		}},
		{"MoveMessage missing folder", func() error {
			_, err := client.MoveMessage(ctx, &mailboxpb.MoveMessageRequest{UserId: "alice", MessageId: "m"})
			return err
		}},
		{"MarkAllRead missing folder", func() error {
			_, err := client.MarkAllRead(ctx, &mailboxpb.MarkAllReadRequest{UserId: "alice"})
			return err
		}},
		{"DeleteDraft missing draft", func() error {
			_, err := client.DeleteDraft(ctx, &mailboxpb.DeleteDraftRequest{UserId: "alice"})
			return err
		}},
		{"UpdateFlags missing message", func() error {
			_, err := client.UpdateFlags(ctx, &mailboxpb.UpdateFlagsRequest{UserId: "alice"})
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.fn()
			if err == nil {
				t.Fatalf("%s: expected InvalidArgument, got nil", c.name)
			}
			if code := status.Code(err); code != codes.InvalidArgument {
				t.Errorf("%s: code = %v, want InvalidArgument", c.name, code)
			}
		})
	}
}

// TestServer_SendMessage_FullFields exercises SendMessage with metadata, TTL,
// and schedule set, plus GetStats/UnreadCount happy paths.
func TestServer_SendMessage_FullFields(t *testing.T) {
	client := startServer(t, server.AllowAll())
	ctx := context.Background()

	md, err := structpb.NewStruct(map[string]any{"category": "billing"})
	if err != nil {
		t.Fatalf("structpb: %v", err)
	}
	resp, err := client.SendMessage(ctx, &mailboxpb.SendMessageRequest{
		UserId:       "alice",
		RecipientIds: []string{"bob"},
		Subject:      "full",
		Body:         "body",
		Headers:      map[string]string{"X-Test": "1"},
		Metadata:     md,
		Ttl:          durationpb.New(time.Hour),
		ScheduleAt:   timestamppb.New(time.Now().Add(time.Minute)),
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp.GetMessage().GetId() == "" {
		t.Error("empty message id")
	}

	if _, err := client.GetStats(ctx, &mailboxpb.GetStatsRequest{UserId: "alice"}); err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if _, err := client.UnreadCount(ctx, &mailboxpb.UnreadCountRequest{UserId: "alice"}); err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
}
