package mailbox_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/notify"
	notifymem "github.com/rbaliyan/mailbox/notify/memory"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
	mongostore "github.com/rbaliyan/mailbox/store/mongo"
	pgstore "github.com/rbaliyan/mailbox/store/postgres"

	"github.com/rbaliyan/event/v3"
	"github.com/rbaliyan/event/v3/transport/channel"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// smokeStoreSeq makes per-test table/collection names process-unique so a
// DSN-gated smoke run never collides with concurrently running integration
// tests against the same database.
var smokeStoreSeq uint64

func uniqueSmokeName(prefix string) string {
	n := atomic.AddUint64(&smokeStoreSeq, 1)
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), n)
}

// sendAndGetOverStore runs the core send->get assertion against an arbitrary
// connected store, used by the production-backend constructor smokes. It builds
// a mailbox service over the store, sends a message from alice to bob, fetches
// it back from bob's inbox, and asserts the subject round-trips.
func sendAndGetOverStore(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()

	svc, err := mailbox.New(mailbox.Config{}, mailbox.WithStore(st))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(ctx) })

	const subject = "Backend smoke"
	if _, err := svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      subject,
		Body:         "body",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	inbox, err := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("Folder(inbox): %v", err)
	}
	msgs := inbox.All()
	if len(msgs) != 1 {
		t.Fatalf("bob inbox count = %d, want 1", len(msgs))
	}

	got, err := svc.Client("bob").Get(ctx, msgs[0].GetID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GetSubject() != subject {
		t.Fatalf("got subject = %q, want %q", got.GetSubject(), subject)
	}
}

// eventuallySmoke polls cond until it returns true or the timeout elapses.
// It fails the test with msg if the condition never holds. This is the
// deterministic alternative to time.Sleep used throughout the smoke suite.
func eventuallySmoke(t *testing.T, timeout, interval time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("eventuallySmoke: condition not met within %s: %s", timeout, msg)
		}
		time.Sleep(interval)
	}
}

// TestSmoke_Lifecycle_MemoryBackend exercises the full happy path:
// New -> Connect -> Client -> Compose -> Send -> recipient Get + inbox count -> Close.
func TestSmoke_Lifecycle_MemoryBackend(t *testing.T) {
	ctx := context.Background()

	// No event transport is wired here, keeping the happy path minimal.
	// (See TestSmoke_Close_ChannelTransport for the channel-transport Close path.)
	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !svc.IsConnected() {
		t.Fatal("expected service to be connected")
	}

	alice := svc.Client("alice")

	draft, err := alice.Compose()
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	draft.SetSubject("Hello").SetBody("World").SetRecipients("bob")
	sent, err := draft.Send(ctx)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sent.GetSubject() != "Hello" {
		t.Fatalf("sent subject = %q, want %q", sent.GetSubject(), "Hello")
	}

	bob := svc.Client("bob")
	inbox, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	if err != nil {
		t.Fatalf("Folder: %v", err)
	}
	if got := len(inbox.All()); got != 1 {
		t.Fatalf("bob inbox count = %d, want 1", got)
	}

	got, err := bob.Get(ctx, inbox.All()[0].GetID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GetSubject() != "Hello" {
		t.Fatalf("got subject = %q, want %q", got.GetSubject(), "Hello")
	}

	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSmoke_CriticalJourney walks the core message lifecycle and asserts state
// at every step: draft -> send -> receive -> mark read -> trash -> restore.
func TestSmoke_CriticalJourney(t *testing.T) {
	ctx := context.Background()
	svc := mailboxtest.NewService(t, mailbox.Config{})

	alice := svc.Client("alice")
	bob := svc.Client("bob")

	// Draft and send.
	draft, err := alice.Compose()
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	draft.SetSubject("Journey").SetBody("body").SetRecipients("bob")
	if _, err := draft.Send(ctx); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Recipient receives it unread.
	inbox := mailboxtest.Inbox(t, bob)
	if len(inbox) != 1 {
		t.Fatalf("inbox count after send = %d, want 1", len(inbox))
	}
	msgID := inbox[0].GetID()
	if inbox[0].GetIsRead() {
		t.Fatal("expected newly received message to be unread")
	}

	// Mark read.
	if err := bob.UpdateFlags(ctx, msgID, mailbox.MarkRead()); err != nil {
		t.Fatalf("UpdateFlags(read): %v", err)
	}
	read, err := bob.Get(ctx, msgID)
	if err != nil {
		t.Fatalf("Get after mark read: %v", err)
	}
	if !read.GetIsRead() {
		t.Fatal("expected message to be read after MarkRead")
	}

	// Soft delete (move to trash).
	if err := bob.Delete(ctx, msgID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := len(mailboxtest.Inbox(t, bob)); got != 0 {
		t.Fatalf("inbox count after delete = %d, want 0", got)
	}
	trash, err := bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	if err != nil {
		t.Fatalf("Folder(trash): %v", err)
	}
	if got := len(trash.All()); got != 1 {
		t.Fatalf("trash count after delete = %d, want 1", got)
	}

	// Restore from trash.
	if err := bob.Restore(ctx, msgID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := len(mailboxtest.Inbox(t, bob)); got != 1 {
		t.Fatalf("inbox count after restore = %d, want 1", got)
	}
	trash, err = bob.Folder(ctx, store.FolderTrash, store.ListOptions{})
	if err != nil {
		t.Fatalf("Folder(trash) after restore: %v", err)
	}
	if got := len(trash.All()); got != 0 {
		t.Fatalf("trash count after restore = %d, want 0", got)
	}
}

// TestSmoke_NotFound_Sentinel verifies that Get on an unknown ID returns the
// mailbox.ErrNotFound sentinel (which itself wraps store.ErrNotFound), so it is
// checkable via errors.Is against either sentinel. The exported sentinel is
// mailbox.ErrNotFound.
func TestSmoke_NotFound_Sentinel(t *testing.T) {
	ctx := context.Background()
	svc := mailboxtest.NewService(t, mailbox.Config{})

	_, err := svc.Client("alice").Get(ctx, "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown message ID, got nil")
	}
	if !errors.Is(err, mailbox.ErrNotFound) {
		t.Fatalf("error = %v, want errors.Is(err, mailbox.ErrNotFound)", err)
	}
	// mailbox.ErrNotFound wraps store.ErrNotFound, so this matches too.
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want errors.Is(err, store.ErrNotFound)", err)
	}
}

// TestSmoke_Search_MemoryBackend verifies the in-memory backend's full-text
// search finds a sent message by a distinctive body term.
func TestSmoke_Search_MemoryBackend(t *testing.T) {
	ctx := context.Background()
	svc := mailboxtest.NewService(t, mailbox.Config{})

	alice := svc.Client("alice")
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Project Alpha update",
		Body:         "The deployment is complete.",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	bob := svc.Client("bob")
	results, err := bob.Search(ctx, store.SearchQuery{Query: "deployment"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	all := results.All()
	if len(all) != 1 {
		t.Fatalf("search results = %d, want 1", len(all))
	}
	if all[0].GetSubject() != "Project Alpha update" {
		t.Fatalf("search hit subject = %q, want %q", all[0].GetSubject(), "Project Alpha update")
	}
}

// TestSmoke_PartialDelivery_Sentinel sends to one over-quota recipient and one
// healthy recipient, asserting IsPartialDelivery reports the failed recipient.
//
// NOTE: an invalid recipient ID is rejected during pre-send validation and
// aborts the whole send (it never becomes a partial delivery). The mechanism
// that genuinely produces per-recipient failure is a rejecting quota — the same
// approach used by Example_partialDelivery — so this test uses that.
func TestSmoke_PartialDelivery_Sentinel(t *testing.T) {
	ctx := context.Background()

	// No event transport: the quota check then reads counts synchronously via
	// store.Count instead of the event-driven stats cache, which would lag
	// behind the filler send and make the rejection nondeterministic.
	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithGlobalQuota(mailbox.QuotaPolicy{
			MaxMessages:  1,
			ExceedAction: mailbox.QuotaActionReject,
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(ctx) })

	// Fill bob's quota so the next delivery to him is rejected.
	if _, err := svc.Client("filler").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "fill",
		Body:         "body",
	}); err != nil {
		t.Fatalf("fill send: %v", err)
	}

	// Send to bob (over quota) and charlie (fine).
	alice := svc.Client("alice")
	msg, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob", "charlie"},
		Subject:      "Partial",
		Body:         "body",
	})
	if err == nil {
		t.Fatal("expected partial delivery error, got nil")
	}

	pde, ok := mailbox.IsPartialDelivery(err)
	if !ok {
		t.Fatalf("error = %v, want a *PartialDeliveryError", err)
	}
	if msg == nil {
		t.Fatal("expected sender message to be returned on partial delivery")
	}
	if len(pde.DeliveredTo) != 1 || pde.DeliveredTo[0] != "charlie" {
		t.Fatalf("DeliveredTo = %v, want [charlie]", pde.DeliveredTo)
	}
	if _, failed := pde.FailedRecipients["bob"]; !failed {
		t.Fatalf("FailedRecipients = %v, want bob to have failed", pde.FailedRecipients)
	}
}

// TestSmoke_DeliverTo_SelectiveInbox sends to {bob, charlie} but only delivers
// locally to bob. Bob must get an inbox copy, charlie must not, yet the message
// still records both recipients.
func TestSmoke_DeliverTo_SelectiveInbox(t *testing.T) {
	ctx := context.Background()
	svc := mailboxtest.NewService(t, mailbox.Config{})

	alice := svc.Client("alice")
	msg, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob", "charlie"},
		DeliverTo:    []string{"bob"},
		Subject:      "Selective",
		Body:         "body",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// The message records the full recipient list.
	recips := msg.GetRecipientIDs()
	if len(recips) != 2 {
		t.Fatalf("RecipientIDs = %v, want both bob and charlie", recips)
	}
	hasBob, hasCharlie := false, false
	for _, r := range recips {
		switch r {
		case "bob":
			hasBob = true
		case "charlie":
			hasCharlie = true
		}
	}
	if !hasBob || !hasCharlie {
		t.Fatalf("RecipientIDs = %v, want both bob and charlie", recips)
	}

	// Bob received an inbox copy.
	if got := len(mailboxtest.Inbox(t, svc.Client("bob"))); got != 1 {
		t.Fatalf("bob inbox count = %d, want 1", got)
	}
	// Charlie did not (delivery was restricted to bob).
	if got := len(mailboxtest.Inbox(t, svc.Client("charlie"))); got != 0 {
		t.Fatalf("charlie inbox count = %d, want 0", got)
	}
}

// TestSmoke_BackgroundMaintenance_StartStop verifies that a service configured
// with non-zero maintenance intervals starts background goroutines that
// actually tick (observable: an eligible trashed message is collected) and that
// Close stops them and returns promptly without hanging.
//
// The minimum legal trash retention is one day (Config normalizes anything
// smaller back to the 30-day default), and the memory store's AgeMessages
// helper is documented as not safe to call while background goroutines are
// running. To satisfy both constraints race-free, the message is created,
// trashed, and aged through a first service that has NO background goroutines;
// that service is closed (the memory store retains its data across Close), and
// only then is a second service connected with the maintenance intervals set.
// Its background tick then sees an already-aged trashed message.
func TestSmoke_BackgroundMaintenance_StartStop(t *testing.T) {
	ctx := context.Background()
	memStore := memory.New()

	// Phase 1: prepare an aged trashed message with no background goroutines.
	prep, err := mailbox.New(mailbox.Config{TrashRetention: 24 * time.Hour},
		mailbox.WithStore(memStore),
	)
	if err != nil {
		t.Fatalf("New(prep): %v", err)
	}
	if err := prep.Connect(ctx); err != nil {
		t.Fatalf("Connect(prep): %v", err)
	}

	alice := prep.Client("alice")
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Old",
		Body:         "body",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	bob := prep.Client("bob")
	inbox := mailboxtest.Inbox(t, bob)
	if len(inbox) != 1 {
		t.Fatalf("inbox count = %d, want 1", len(inbox))
	}
	msgID := inbox[0].GetID()
	if err := bob.Delete(ctx, msgID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Age the trashed copy past the 24h retention. Safe: no goroutine is
	// concurrently reading the store (prep has no background tasks).
	memStore.AgeMessagesByID(48*time.Hour, msgID)

	// Close prep. The memory store flips its connected flag but retains data.
	if err := prep.Close(ctx); err != nil {
		t.Fatalf("Close(prep): %v", err)
	}

	// Phase 2: connect a service with maintenance intervals over the same store.
	svc, err := mailbox.New(mailbox.Config{
		TrashCleanupInterval:          20 * time.Millisecond,
		ExpiredMessageCleanupInterval: 20 * time.Millisecond,
		TrashRetention:                24 * time.Hour,
	},
		mailbox.WithStore(memStore),
	)
	if err != nil {
		t.Fatalf("New(svc): %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("Connect(svc): %v", err)
	}

	// A background trash-cleanup tick should collect the aged message.
	bob2 := svc.Client("bob")
	eventuallySmoke(t, 2*time.Second, 10*time.Millisecond, func() bool {
		trash, err := bob2.Folder(ctx, store.FolderTrash, store.ListOptions{})
		if err != nil {
			return false
		}
		return len(trash.All()) == 0
	}, "background trash cleanup did not collect the aged message")

	// Close must stop the background goroutines and return promptly.
	done := make(chan error, 1)
	go func() { done <- svc.Close(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close(svc): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return; background goroutines may be hanging")
	}
}

// TestSmoke_Close_ChannelTransport verifies that closing a service which owns a
// channel-transport event bus returns nil. The bus closes its transport before
// unregistering subscriptions, so those unregister calls report
// transport.ErrTransportClosed; Close treats those benign shutdown errors as
// success while still surfacing any genuine close failure.
func TestSmoke_Close_ChannelTransport(t *testing.T) {
	ctx := context.Background()

	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if closeErr := svc.Close(ctx); closeErr != nil {
		t.Fatalf("Close with a channel transport returned %v, want nil", closeErr)
	}
}

// TestSmoke_NotifierDelivery wires a notifier with an in-memory notify store,
// opens a per-user stream, sends a message, and blocks on stream.Next to
// receive the delivered event (no sleep).
func TestSmoke_NotifierDelivery(t *testing.T) {
	ctx := context.Background()

	notifier := notify.NewNotifier(
		notify.WithStore(notifymem.New()),
		notify.WithPollInterval(20*time.Millisecond),
	)
	svc := mailboxtest.NewService(t, mailbox.Config{},
		mailbox.WithNotifier(notifier),
	)

	// Open bob's stream before sending so no event is missed.
	stream, err := svc.Notifications(ctx, "bob", "")
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	defer stream.Close()

	alice := svc.Client("alice")
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Real-time",
		Body:         "body",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Block on Next (with a deadline) until the received event arrives.
	// Several events may fire; select the message-received one.
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		evt, err := stream.Next(readCtx)
		if err != nil {
			t.Fatalf("stream.Next: %v", err)
		}
		if evt.Type == mailbox.EventNameMessageReceived {
			if evt.UserID != "bob" {
				t.Fatalf("event UserID = %q, want bob", evt.UserID)
			}
			return
		}
	}
}

// TestSmoke_OutboxRelay_MemoryStore exercises the outbox path only if the
// memory store actually enables it. The memory store reports
// OutboxEnabled() == false (its WithOutboxCtx is a direct pass-through with no
// transactional outbox), so there is no relay path to exercise and the test
// skips with a clear note rather than asserting a no-op.
//
// When a transactional outbox-capable store is wired here in the future, the
// skip branch below should be replaced with: enable the outbox, send a message,
// and assert the event is observed via a channel subscribe after relay.
func TestSmoke_OutboxRelay_MemoryStore(t *testing.T) {
	s := memory.New()
	if !s.OutboxEnabled() {
		t.Skip("memory store OutboxEnabled() == false: no transactional outbox to exercise (WithOutboxCtx is a no-op pass-through)")
	}

	// Outbox-capable path: send a message and observe the relayed event.
	ctx := context.Background()
	svc := mailboxtest.NewServiceWithStore(t, mailbox.Config{}, s)

	got := make(chan string, 4)
	svc.Events().MessageSent.Subscribe(ctx,
		func(_ context.Context, _ event.Event[mailbox.MessageSentEvent], data mailbox.MessageSentEvent) error {
			got <- data.Subject
			return nil
		},
	)

	alice := svc.Client("alice")
	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Outbox",
		Body:         "body",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	select {
	case subject := <-got:
		if subject != "Outbox" {
			t.Fatalf("relayed event subject = %q, want %q", subject, "Outbox")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not observe relayed MessageSent event")
	}
}

// TestSmoke_PostgresConstructor exercises the PostgreSQL store construction path
// (pgstore.New + Connect + send/get) against a real database. It is hermetic by
// default: with POSTGRES_DSN unset it skips, keeping the fast smoke run free of
// external dependencies while still compiling in the default (no-tag) build.
//
// Set POSTGRES_DSN to run it, e.g.:
//
//	POSTGRES_DSN=postgres://mailbox_test:mailbox_test@localhost:5433/mailbox_test?sslmode=disable \
//	    go test -run TestSmoke_PostgresConstructor .
func TestSmoke_PostgresConstructor(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set POSTGRES_DSN to run the PostgreSQL constructor smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := sqlx.ConnectContext(ctx, "postgres", dsn)
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	table := uniqueSmokeName("smoke_messages")
	outboxTable := uniqueSmokeName("smoke_outbox")
	st := pgstore.New(db,
		pgstore.WithTable(table),
		pgstore.WithOutboxTable(outboxTable),
	)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = db.ExecContext(cctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
		_, _ = db.ExecContext(cctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", outboxTable))
	})

	sendAndGetOverStore(t, st)
}

// TestSmoke_MongoConstructor exercises the MongoDB store construction path
// (mongostore.New + Connect + send/get) against a real database. Like the
// PostgreSQL smoke it skips when MONGO_URI is unset, so the default smoke run
// stays hermetic while the construction path is covered when a URI is provided.
//
// Set MONGO_URI to run it, e.g.:
//
//	MONGO_URI=mongodb://localhost:27019/?directConnection=true \
//	    go test -run TestSmoke_MongoConstructor .
func TestSmoke_MongoConstructor(t *testing.T) {
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("set MONGO_URI to run the MongoDB constructor smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(mongoopts.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("mongo connect: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("mongo ping: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	coll := uniqueSmokeName("smoke_messages")
	st := mongostore.New(client,
		mongostore.WithDatabase("mailbox_smoke"),
		mongostore.WithCollection(coll),
		mongostore.WithOutboxCollection(coll+"_outbox"),
	)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_ = client.Database("mailbox_smoke").Collection(coll).Drop(cctx)
		_ = client.Database("mailbox_smoke").Collection(coll + "_outbox").Drop(cctx)
	})

	sendAndGetOverStore(t, st)
}
