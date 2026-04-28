package mailbox

import (
	"context"
	"sort"
	"testing"

	"github.com/rbaliyan/event/v3/transport/channel"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func newThreadTestService(t *testing.T) Service {
	t.Helper()
	svc, err := New(Config{}, WithStore(memory.New()), WithEventTransport(channel.New()))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// --- resolveThreadID / auto-generation ---

func TestSend_AutoGeneratesThreadID(t *testing.T) {
	svc := newThreadTestService(t)
	msg, err := svc.Client("alice").SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.GetThreadID() == "" {
		t.Fatal("expected non-empty thread ID on sent message")
	}
}

func TestSend_AllCopiesShareThreadID(t *testing.T) {
	svc := newThreadTestService(t)
	sent, err := svc.Client("alice").SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	threadID := sent.GetThreadID()

	// Bob's inbox copy must carry the same thread ID.
	inbox, err := svc.Client("bob").Folder(context.Background(), store.FolderInbox, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	msgs := inbox.All()
	if len(msgs) == 0 {
		t.Fatal("bob has no messages")
	}
	if msgs[0].GetThreadID() != threadID {
		t.Errorf("bob's copy threadID=%q, want %q", msgs[0].GetThreadID(), threadID)
	}
}

func TestSend_ReplyInheritsThreadID(t *testing.T) {
	svc := newThreadTestService(t)
	alice := svc.Client("alice")
	bob := svc.Client("bob")

	orig, err := alice.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	threadID := orig.GetThreadID()

	// Find bob's inbox copy (he owns it).
	inbox, err := bob.Folder(context.Background(), store.FolderInbox, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	bobMsgID := inbox.All()[0].GetID()

	// Bob replies using the inbox copy ID as ReplyToID.
	reply, err := bob.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"alice"},
		ReplyToID:    bobMsgID,
		Subject:      "Re: Hello",
		Body:         "Hi",
	})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply.GetThreadID() != threadID {
		t.Errorf("reply threadID=%q, want %q", reply.GetThreadID(), threadID)
	}
}

func TestSend_ReplyToUnownedMessageStartsNewThread(t *testing.T) {
	// resolveThreadID must not inherit thread ID from a message the sender
	// does not own — prevents thread-ID enumeration across user boundaries.
	svc := newThreadTestService(t)
	alice := svc.Client("alice")

	// Alice sends to bob. Alice's sent copy is owned by alice; bob's inbox
	// copy is owned by bob. The message IDs are different.
	orig, err := alice.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	aliceMsgID := orig.GetID() // alice owns this (sent copy)

	// Carol tries to reply using alice's sent-copy ID as ReplyToID.
	// Carol does NOT own that message → resolveThreadID must fall through
	// to a new UUID rather than leaking alice's thread ID.
	carol := svc.Client("carol")
	carolMsg, err := carol.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"dave"},
		ReplyToID:    aliceMsgID, // carol doesn't own this
		Subject:      "Sneaky reply",
		Body:         "body",
	})
	if err != nil {
		t.Fatalf("carol send: %v", err)
	}
	if carolMsg.GetThreadID() == orig.GetThreadID() {
		t.Error("carol should NOT inherit alice's thread ID for a message she doesn't own")
	}
	if carolMsg.GetThreadID() == "" {
		t.Error("carol's message should still have a thread ID (new UUID)")
	}
}

func TestSend_ReplyToLegacyMessageUsesParentIDAsThreadRoot(t *testing.T) {
	// When the parent message has an empty thread_id (pre-thread-generation
	// legacy message), the first reply should use the parent's message ID
	// as the thread root — matching draft.ReplyTo behaviour.
	svc := newThreadTestService(t)
	st := svc.(*service).store

	// Directly insert a legacy message with no thread ID.
	legacy, err := st.CreateMessage(context.Background(), store.MessageData{
		OwnerID:  "alice",
		SenderID: "external",
		Subject:  "Legacy",
		Body:     "No thread ID",
		FolderID: store.FolderInbox,
		ThreadID: "", // deliberately empty
	})
	if err != nil {
		t.Fatalf("create legacy: %v", err)
	}

	// Alice replies — parent has no thread ID, so legacyID becomes the root.
	reply, err := svc.Client("alice").SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		ReplyToID:    legacy.GetID(),
		Subject:      "Re: Legacy",
		Body:         "body",
	})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply.GetThreadID() != legacy.GetID() {
		t.Errorf("threadID=%q, want parent ID %q", reply.GetThreadID(), legacy.GetID())
	}
}

func TestSend_ExplicitThreadIDAlwaysWins(t *testing.T) {
	svc := newThreadTestService(t)
	msg, err := svc.Client("alice").SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		ThreadID:     "explicit-thread-123",
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.GetThreadID() != "explicit-thread-123" {
		t.Errorf("threadID=%q, want %q", msg.GetThreadID(), "explicit-thread-123")
	}
}

func TestSend_DraftReplyToAndSendMessageThreadIDAlign(t *testing.T) {
	// draft.ReplyTo and SendMessage{ReplyToID} must produce the same thread ID
	// for the same reply target.
	svc := newThreadTestService(t)
	alice := svc.Client("alice")
	bob := svc.Client("bob")

	orig, err := alice.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	threadID := orig.GetThreadID()

	// Find bob's inbox copy.
	inbox, err := bob.Folder(context.Background(), store.FolderInbox, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	bobMsgID := inbox.All()[0].GetID()

	// Reply via SendMessage.
	reply1, err := bob.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"alice"},
		ReplyToID:    bobMsgID,
		Subject:      "Re: Hello",
		Body:         "via SendMessage",
	})
	if err != nil {
		t.Fatalf("sendmessage reply: %v", err)
	}

	// Reply via draft.ReplyTo.
	draft, err := bob.Compose()
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if err := draft.ReplyTo(context.Background(), bobMsgID); err != nil {
		t.Fatalf("draft.ReplyTo: %v", err)
	}
	draft.SetRecipients("alice").SetSubject("Re: Hello").SetBody("via draft")
	reply2, err := draft.Send(context.Background())
	if err != nil {
		t.Fatalf("draft send: %v", err)
	}

	if reply1.GetThreadID() != threadID {
		t.Errorf("SendMessage reply threadID=%q, want %q", reply1.GetThreadID(), threadID)
	}
	if reply2.GetThreadID() != threadID {
		t.Errorf("draft reply threadID=%q, want %q", reply2.GetThreadID(), threadID)
	}
}

// --- ThreadParticipants ---

func TestThreadParticipants_ReturnsAllParticipants(t *testing.T) {
	svc := newThreadTestService(t)
	alice := svc.Client("alice")
	bob := svc.Client("bob")

	msg, err := alice.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	threadID := msg.GetThreadID()

	// Find bob's inbox copy so he can use it as reply target.
	inbox, err := bob.Folder(context.Background(), store.FolderInbox, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if _, err := bob.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"alice"},
		ReplyToID:    inbox.All()[0].GetID(),
		Subject:      "Re: Hello",
		Body:         "Hi Alice",
	}); err != nil {
		t.Fatalf("reply: %v", err)
	}

	participants, err := svc.ThreadParticipants(context.Background(), threadID)
	if err != nil {
		t.Fatalf("ThreadParticipants: %v", err)
	}

	sort.Strings(participants)
	if len(participants) != 2 || participants[0] != "alice" || participants[1] != "bob" {
		t.Errorf("got participants %v, want [alice bob]", participants)
	}
}

func TestThreadParticipants_ExcludesTrashedMessages(t *testing.T) {
	svc := newThreadTestService(t)
	alice := svc.Client("alice")
	bob := svc.Client("bob")

	msg, err := alice.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Hello",
		Body:         "World",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	threadID := msg.GetThreadID()

	// Bob deletes his inbox copy (moves to trash).
	inbox, err := bob.Folder(context.Background(), store.FolderInbox, store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	for _, m := range inbox.All() {
		if m.GetThreadID() == threadID {
			if err := bob.Delete(context.Background(), m.GetID()); err != nil {
				t.Fatalf("delete: %v", err)
			}
		}
	}

	participants, err := svc.ThreadParticipants(context.Background(), threadID)
	if err != nil {
		t.Fatalf("ThreadParticipants: %v", err)
	}

	// Alice must be present (her sent copy is not trashed).
	found := false
	for _, p := range participants {
		if p == "alice" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected alice in participants, got %v", participants)
	}
}

func TestThreadParticipants_NotFound(t *testing.T) {
	svc := newThreadTestService(t)

	_, err := svc.ThreadParticipants(context.Background(), "nonexistent-thread-id")
	if err == nil {
		t.Fatal("expected error for nonexistent thread")
	}
}

func TestThreadParticipants_EmptyThreadID(t *testing.T) {
	svc := newThreadTestService(t)

	_, err := svc.ThreadParticipants(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty thread ID")
	}
}

func TestThreadParticipants_SMTPGatewayPattern(t *testing.T) {
	// Simulate the SMTP gateway pattern: external system knows thread_id,
	// uses ThreadParticipants to find recipients, then delivers.
	svc := newThreadTestService(t)
	alice := svc.Client("alice")

	msg, err := alice.SendMessage(context.Background(), SendRequest{
		RecipientIDs: []string{"bob", "carol"},
		Subject:      "Team update",
		Body:         "FYI",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	threadID := msg.GetThreadID()

	// SMTP gateway receives an external reply. It knows the thread_id
	// (e.g., from X-Mailbox-Thread-ID header) but not the recipients.
	participants, err := svc.ThreadParticipants(context.Background(), threadID)
	if err != nil {
		t.Fatalf("ThreadParticipants: %v", err)
	}

	_, err = svc.Client("smtp-gateway").SendMessage(context.Background(), SendRequest{
		RecipientIDs: participants,
		ThreadID:     threadID,
		Subject:      "Re: Team update",
		Body:         "Got it, thanks",
	})
	if err != nil {
		t.Fatalf("smtp delivery: %v", err)
	}

	// All original participants should now have ≥2 messages in the thread.
	for _, userID := range []string{"alice", "bob", "carol"} {
		mb := svc.Client(userID)
		thread, err := mb.GetThread(context.Background(), threadID, store.ListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("GetThread for %s: %v", userID, err)
		}
		if len(thread.All()) < 2 {
			t.Errorf("user %s: expected ≥2 messages in thread, got %d", userID, len(thread.All()))
		}
	}
}
