package mailbox_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/store"
)

// noopAttachmentManager is a permissive store.AttachmentManager that lets
// attachment ref-counting succeed so the send path can be exercised end to end
// without a real attachment backend.
type noopAttachmentManager struct{}

func (noopAttachmentManager) Upload(context.Context, string, string, string, io.Reader) (store.AttachmentMetadata, error) {
	return nil, nil
}
func (noopAttachmentManager) Load(context.Context, string) (io.ReadCloser, error)          { return nil, nil }
func (noopAttachmentManager) GetMetadata(context.Context, string) (store.AttachmentMetadata, error) {
	return nil, nil
}
func (noopAttachmentManager) AddRef(context.Context, string) error    { return nil }
func (noopAttachmentManager) RemoveRef(context.Context, string) error { return nil }

// draftAtt is a minimal store.Attachment used to exercise AddAttachment.
type draftAtt struct {
	name        string
	contentType string
	size        int64
}

func (a draftAtt) GetID() string          { return "att-" + a.name }
func (a draftAtt) GetFilename() string     { return a.name }
func (a draftAtt) GetContentType() string  { return a.contentType }
func (a draftAtt) GetSize() int64          { return a.size }
func (a draftAtt) GetURI() string          { return "mem://" + a.name }
func (a draftAtt) GetCreatedAt() time.Time { return time.Now() }

// TestDraft_FluentSendFlow exercises the full fluent compose-and-send path
// along with all read accessors on the resulting draft.
func TestDraft_FluentSendFlow(t *testing.T) {
	ctx := context.Background()
	svc := newAttachSvc(t, noopAttachmentManager{})
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	draft, err := alice.Compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}

	draft.SetSubject("Quarterly Report").
		SetBody("Please review the attached figures.").
		SetRecipients("bob", "carol").
		SetDeliverTo("bob").
		SetExternalID("ext-123").
		SetHeader("X-Priority", "high").
		SetMetadata("category", "finance")

	if err := draft.AddAttachment(draftAtt{name: "q.pdf", contentType: "application/pdf", size: 2048}); err != nil {
		t.Fatalf("add attachment: %v", err)
	}

	// Accessors reflect the composed state.
	if got := draft.OwnerID(); got != "alice" {
		t.Errorf("OwnerID = %q, want alice", got)
	}
	if got := draft.Subject(); got != "Quarterly Report" {
		t.Errorf("Subject = %q", got)
	}
	if got := draft.Body(); got != "Please review the attached figures." {
		t.Errorf("Body = %q", got)
	}
	if got := draft.RecipientIDs(); len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
		t.Errorf("RecipientIDs = %v", got)
	}
	if got := draft.DeliverTo(); len(got) != 1 || got[0] != "bob" {
		t.Errorf("DeliverTo = %v", got)
	}
	if got := draft.ExternalID(); got != "ext-123" {
		t.Errorf("ExternalID = %q", got)
	}
	if got := draft.Headers(); got["X-Priority"] != "high" {
		t.Errorf("Headers = %v", got)
	}
	if got := draft.Metadata(); got["category"] != "finance" {
		t.Errorf("Metadata = %v", got)
	}
	if got := draft.Attachments(); len(got) != 1 || got[0].GetFilename() != "q.pdf" {
		t.Errorf("Attachments = %v", got)
	}
	if got := draft.ThreadID(); got != "" {
		t.Errorf("ThreadID = %q, want empty for a fresh draft", got)
	}
	if got := draft.ReplyToID(); got != "" {
		t.Errorf("ReplyToID = %q, want empty for a fresh draft", got)
	}

	// Send returns the sender's copy.
	msg, err := draft.Send(ctx)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.GetSubject() != "Quarterly Report" {
		t.Errorf("sent subject = %q", msg.GetSubject())
	}

	// Only bob received an inbox copy (DeliverTo restricts delivery).
	bob := svc.Client("bob")
	bobMsg := firstInbox(t, bob)
	if bobMsg.GetSubject() != "Quarterly Report" {
		t.Errorf("bob subject = %q", bobMsg.GetSubject())
	}
	// The stored recipient list retains everyone.
	if rcpts := bobMsg.GetRecipientIDs(); len(rcpts) != 2 {
		t.Errorf("recipient list = %v, want 2", rcpts)
	}
}

// TestDraft_Forward covers preparing a draft as a forward of an existing
// message, copying subject (Fwd: prefix), body, and attachments.
func TestDraft_Forward(t *testing.T) {
	ctx := context.Background()
	svc := newAttachSvc(t, noopAttachmentManager{})
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")

	// Alice sends bob a message with an attachment.
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Original",
		Body:         "original body",
		Attachments:  []store.Attachment{draftAtt{name: "orig.txt", contentType: "text/plain", size: 10}},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	bobMsg := firstInbox(t, bob)

	// Bob forwards it to carol.
	fwd, err := bob.Compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if err := fwd.Forward(ctx, bobMsg.GetID()); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if fwd.Subject() != "Fwd: Original" {
		t.Errorf("forward subject = %q, want Fwd: Original", fwd.Subject())
	}
	if fwd.Body() != "original body" {
		t.Errorf("forward body = %q", fwd.Body())
	}
	if len(fwd.Attachments()) != 1 {
		t.Errorf("forward attachments = %d, want 1", len(fwd.Attachments()))
	}

	// Forwarding again does not double-prefix.
	fwd2, _ := bob.Compose()
	if err := fwd2.Forward(ctx, bobMsg.GetID()); err != nil {
		t.Fatalf("forward2: %v", err)
	}
	if err := fwd2.Forward(ctx, bobMsg.GetID()); err != nil {
		t.Fatalf("re-forward: %v", err)
	}

	fwd.SetRecipients("carol")
	if _, err := fwd.Send(ctx); err != nil {
		t.Fatalf("send forward: %v", err)
	}
	carol := svc.Client("carol")
	cm := firstInbox(t, carol)
	if cm.GetSubject() != "Fwd: Original" {
		t.Errorf("carol subject = %q", cm.GetSubject())
	}
}

// TestDraft_Forward_Errors covers the failure branches of Forward.
func TestDraft_Forward_Errors(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	d, err := svc.Client("alice").Compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if err := d.Forward(ctx, ""); err == nil {
		t.Error("expected error forwarding with empty message ID")
	}
	if err := d.Forward(ctx, "nonexistent"); err == nil {
		t.Error("expected error forwarding nonexistent message")
	}
}

// TestDraft_Delete covers Delete for both saved and unsaved drafts.
func TestDraft_Delete(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")

	// Unsaved draft: Delete is a no-op and does not error.
	unsaved, err := alice.Compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	unsaved.SetSubject("scratch")
	if err := unsaved.Delete(ctx); err != nil {
		t.Errorf("delete unsaved draft: %v", err)
	}

	// Saved draft: Delete removes it from storage.
	saved, err := alice.Compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	saved.SetSubject("work in progress").SetBody("draft body")
	persisted, err := saved.Save(ctx)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if persisted.ID() == "" {
		t.Fatal("saved draft has empty ID")
	}
	if err := persisted.Delete(ctx); err != nil {
		t.Fatalf("delete saved draft: %v", err)
	}
}

// TestDraft_AddAttachment_Validation covers the validation branches in
// AddAttachment.
func TestDraft_AddAttachment_Validation(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	d, err := svc.Client("alice").Compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}

	if err := d.AddAttachment(nil); err == nil {
		t.Error("expected error adding nil attachment")
	}
	if err := d.AddAttachment(draftAtt{name: "", contentType: "text/plain", size: 1}); err == nil {
		t.Error("expected error for empty filename")
	}
	if err := d.AddAttachment(draftAtt{name: "x.txt", contentType: "", size: 1}); err == nil {
		t.Error("expected error for empty content type")
	}
	// Valid attachment succeeds.
	if err := d.AddAttachment(draftAtt{name: "ok.txt", contentType: "text/plain", size: 1}); err != nil {
		t.Errorf("valid attachment rejected: %v", err)
	}
}

// TestDraft_ReplyTo threads a reply onto an existing message.
func TestDraft_ReplyTo(t *testing.T) {
	ctx := context.Background()
	svc := newTestService()
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")

	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Question",
		Body:         "What time?",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	bobMsg := firstInbox(t, bob)

	reply, err := bob.Compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if err := reply.ReplyTo(ctx, bobMsg.GetID()); err != nil {
		t.Fatalf("reply to: %v", err)
	}
	if reply.ReplyToID() != bobMsg.GetID() {
		t.Errorf("ReplyToID = %q, want %q", reply.ReplyToID(), bobMsg.GetID())
	}
	if reply.ThreadID() == "" {
		t.Error("ThreadID should be set after ReplyTo")
	}

	// Empty message ID is rejected.
	if err := reply.ReplyTo(ctx, ""); err == nil {
		t.Error("expected error for empty reply target")
	}
}
