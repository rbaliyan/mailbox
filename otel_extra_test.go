package mailbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/rbaliyan/event/v3/transport/channel"
	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/mailboxtest"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestOTelInstrumentation wires a real SDK tracer and meter provider with OTel
// enabled and drives the instrumented operations (send, read, move, delete) so
// the otel.go record/startSpan/initMetrics paths are exercised.
func TestOTelInstrumentation(t *testing.T) {
	ctx := context.Background()

	tp := sdktrace.NewTracerProvider()
	mp := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
	})

	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
		mailbox.WithOTel(true),
		mailbox.WithTracing(true),
		mailbox.WithMetrics(true),
		mailbox.WithServiceName("otel-test"),
		mailbox.WithTracerProvider(tp),
		mailbox.WithMeterProvider(mp),
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	bob := svc.Client("bob")

	if _, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "instrumented",
		Body:         "body",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	var msg mailbox.Message
	mailboxtest.Eventually(t, time.Second, 5*time.Millisecond, func() bool {
		list, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{Limit: 10})
		if err != nil || len(list.All()) == 0 {
			return false
		}
		msg = list.All()[0]
		return true
	}, "inbox copy never arrived")

	// Drive instrumented mutations.
	if err := bob.UpdateFlags(ctx, msg.GetID(), mailbox.MarkRead()); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if err := bob.MoveToFolder(ctx, msg.GetID(), store.FolderArchived); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err := bob.AddTag(ctx, msg.GetID(), "tg"); err != nil {
		t.Fatalf("add tag: %v", err)
	}
	if err := bob.RemoveTag(ctx, msg.GetID(), "tg"); err != nil {
		t.Fatalf("remove tag: %v", err)
	}
	if err := bob.Delete(ctx, msg.GetID()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := bob.Stats(ctx); err != nil {
		t.Fatalf("stats: %v", err)
	}
}
