// Package mailboxtest provides test helpers for mailbox integration tests.
//
// These helpers reduce boilerplate in tests that use the mailbox library.
// All functions accept a *testing.T and call t.Fatal on errors.
//
// Example:
//
//	func TestSomething(t *testing.T) {
//	    svc := mailboxtest.NewService(t)
//	    defer svc.Close(context.Background())
//
//	    alice := svc.Client("alice")
//	    msg := mailboxtest.SendMessage(t, alice, "bob", "Hello", "World")
//	    // ...
//	}
package mailboxtest

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/rbaliyan/event/v3/transport/channel"
	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
	"golang.org/x/crypto/curve25519"
)

// NewService creates a connected mailbox service with in-memory store and
// channel event transport. The service is ready to use immediately.
func NewService(t *testing.T, opts ...mailbox.Option) mailbox.Service {
	t.Helper()
	allOpts := append([]mailbox.Option{
		mailbox.WithStore(memory.New()),
		mailbox.WithEventTransport(channel.New()),
	}, opts...)
	svc, err := mailbox.NewService(allOpts...)
	if err != nil {
		t.Fatalf("mailboxtest: create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("mailboxtest: connect: %v", err)
	}
	t.Cleanup(func() { svc.Close(context.Background()) })
	return svc
}

// NewMemoryStore creates a connected in-memory store.
// Useful when you need direct store access (e.g., for AgeMessages).
func NewMemoryStore(t *testing.T) *memory.Store {
	t.Helper()
	s := memory.New()
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("mailboxtest: connect store: %v", err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

// NewServiceWithStore creates a connected service using the given store.
// The store must already be connected.
func NewServiceWithStore(t *testing.T, s store.Store, opts ...mailbox.Option) mailbox.Service {
	t.Helper()
	allOpts := append([]mailbox.Option{
		mailbox.WithStore(s),
		mailbox.WithEventTransport(channel.New()),
	}, opts...)
	svc, err := mailbox.NewService(allOpts...)
	if err != nil {
		t.Fatalf("mailboxtest: create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("mailboxtest: connect: %v", err)
	}
	t.Cleanup(func() { svc.Close(context.Background()) })
	return svc
}

// SendMessage sends a simple text message and returns it.
func SendMessage(t *testing.T, sender mailbox.Mailbox, recipientID, subject, body string) mailbox.Message {
	t.Helper()
	msg, err := sender.SendMessage(context.Background(), mailbox.SendRequest{
		RecipientIDs: []string{recipientID},
		Subject:      subject,
		Body:         body,
	})
	if err != nil {
		t.Fatalf("mailboxtest: send message: %v", err)
	}
	return msg
}

// X25519Keypair generates a random X25519 keypair for testing.
// Returns (publicKey, privateKey) as 32-byte slices.
func X25519Keypair(t *testing.T) (pub, priv []byte) {
	t.Helper()
	priv = make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatalf("mailboxtest: generate keypair: %v", err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("mailboxtest: derive public key: %v", err)
	}
	return pub, priv
}

// Inbox returns all messages in a user's inbox.
func Inbox(t *testing.T, mb mailbox.Mailbox) []mailbox.Message {
	t.Helper()
	list, err := mb.Folder(context.Background(), store.FolderInbox, store.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("mailboxtest: list inbox: %v", err)
	}
	return list.All()
}
