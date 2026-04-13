package mailbox

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// testUser is a simple User implementation for tests.
type testUser struct {
	firstName string
	lastName  string
	email     string
}

func (u *testUser) FirstName() string { return u.firstName }
func (u *testUser) LastName() string  { return u.lastName }
func (u *testUser) Email() string     { return u.email }

// testUserResolver is a map-based UserResolver for tests.
type testUserResolver struct {
	users map[string]*testUser
}

func (r *testUserResolver) ResolveUser(_ context.Context, userID string) (User, error) {
	u, ok := r.users[userID]
	if !ok {
		return nil, fmt.Errorf("user not found: %s", userID)
	}
	return u, nil
}

// failingUserResolver always returns an error.
type failingUserResolver struct {
	err error
}

func (r *failingUserResolver) ResolveUser(_ context.Context, _ string) (User, error) {
	return nil, r.err
}

func setupServiceWithUserResolver(t *testing.T, resolver UserResolver) Service {
	t.Helper()
	opts := []Option{
		WithStore(memory.New()),
		WithUserResolver(resolver),
	}
	svc, err := NewService(opts...)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	return svc
}

func TestUserResolverIntegration(t *testing.T) {
	ctx := context.Background()

	resolver := &testUserResolver{
		users: map[string]*testUser{
			"alice": {firstName: "Alice", lastName: "Smith", email: "alice@example.com"},
			"bob":   {firstName: "Bob", lastName: "Jones", email: "bob@example.com"},
		},
	}

	svc := setupServiceWithUserResolver(t, resolver)
	defer svc.Close(ctx)

	t.Run("Compose+Send populates sender metadata", func(t *testing.T) {
		alice := svc.Client("alice")
		bob := svc.Client("bob")

		draft := mustCompose(alice)
		draft.SetSubject("Hello Bob").
			SetBody("Test body").
			SetRecipients("bob")

		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		// Sender's copy should have sender metadata
		meta := msg.GetMetadata()
		if meta[MetadataSenderFirstName] != "Alice" {
			t.Errorf("sender copy: sender.firstname = %v, want Alice", meta[MetadataSenderFirstName])
		}
		if meta[MetadataSenderLastName] != "Smith" {
			t.Errorf("sender copy: sender.lastname = %v, want Smith", meta[MetadataSenderLastName])
		}
		if meta[MetadataSenderEmail] != "alice@example.com" {
			t.Errorf("sender copy: sender.email = %v, want alice@example.com", meta[MetadataSenderEmail])
		}

		// Recipient's copy should also have sender metadata
		inbox, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if err != nil {
			t.Fatalf("list inbox failed: %v", err)
		}
		if len(inbox.All()) != 1 {
			t.Fatalf("expected 1 inbox message, got %d", len(inbox.All()))
		}
		recipientMeta := inbox.All()[0].GetMetadata()
		if recipientMeta[MetadataSenderFirstName] != "Alice" {
			t.Errorf("recipient copy: sender.firstname = %v, want Alice", recipientMeta[MetadataSenderFirstName])
		}
		if recipientMeta[MetadataSenderLastName] != "Smith" {
			t.Errorf("recipient copy: sender.lastname = %v, want Smith", recipientMeta[MetadataSenderLastName])
		}
		if recipientMeta[MetadataSenderEmail] != "alice@example.com" {
			t.Errorf("recipient copy: sender.email = %v, want alice@example.com", recipientMeta[MetadataSenderEmail])
		}
	})

	t.Run("SendMessage populates sender metadata", func(t *testing.T) {
		bob := svc.Client("bob")

		msg, err := bob.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"alice"},
			Subject:      "Hello Alice",
			Body:         "From Bob",
		})
		if err != nil {
			t.Fatalf("SendMessage failed: %v", err)
		}

		meta := msg.GetMetadata()
		if meta[MetadataSenderFirstName] != "Bob" {
			t.Errorf("sender.firstname = %v, want Bob", meta[MetadataSenderFirstName])
		}
		if meta[MetadataSenderLastName] != "Jones" {
			t.Errorf("sender.lastname = %v, want Jones", meta[MetadataSenderLastName])
		}
		if meta[MetadataSenderEmail] != "bob@example.com" {
			t.Errorf("sender.email = %v, want bob@example.com", meta[MetadataSenderEmail])
		}
	})

	t.Run("preserves existing metadata", func(t *testing.T) {
		alice := svc.Client("alice")

		draft := mustCompose(alice)
		draft.SetSubject("With custom metadata").
			SetBody("Test").
			SetRecipients("bob").
			SetMetadata("priority", "high").
			SetMetadata("category", "work")

		msg, err := draft.Send(ctx)
		if err != nil {
			t.Fatalf("send failed: %v", err)
		}

		meta := msg.GetMetadata()
		// User metadata should be present
		if meta[MetadataSenderFirstName] != "Alice" {
			t.Errorf("sender.firstname = %v, want Alice", meta[MetadataSenderFirstName])
		}
		// Custom metadata should be preserved
		if meta["priority"] != "high" {
			t.Errorf("priority = %v, want high", meta["priority"])
		}
		if meta["category"] != "work" {
			t.Errorf("category = %v, want work", meta["category"])
		}
	})
}

func TestUserResolverFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("resolve error aborts Compose+Send", func(t *testing.T) {
		resolver := &failingUserResolver{err: errors.New("database unavailable")}
		svc := setupServiceWithUserResolver(t, resolver)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		draft := mustCompose(alice)
		draft.SetSubject("Should fail").
			SetBody("Test").
			SetRecipients("bob")

		_, err := draft.Send(ctx)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrUserResolveFailed) {
			t.Errorf("expected ErrUserResolveFailed, got %v", err)
		}
	})

	t.Run("resolve error aborts SendMessage", func(t *testing.T) {
		resolver := &failingUserResolver{err: errors.New("connection refused")}
		svc := setupServiceWithUserResolver(t, resolver)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Should fail",
			Body:         "Test",
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrUserResolveFailed) {
			t.Errorf("expected ErrUserResolveFailed, got %v", err)
		}
	})

	t.Run("unknown sender aborts send", func(t *testing.T) {
		resolver := &testUserResolver{
			users: map[string]*testUser{
				"bob": {firstName: "Bob", lastName: "Jones", email: "bob@example.com"},
			},
		}
		svc := setupServiceWithUserResolver(t, resolver)
		defer svc.Close(ctx)

		// "alice" is not in the resolver
		alice := svc.Client("alice")
		_, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Should fail",
			Body:         "Test",
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrUserResolveFailed) {
			t.Errorf("expected ErrUserResolveFailed, got %v", err)
		}
	})

	t.Run("no message created on resolve failure", func(t *testing.T) {
		resolver := &failingUserResolver{err: errors.New("fail")}
		svc := setupServiceWithUserResolver(t, resolver)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		bob := svc.Client("bob")

		_, _ = alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"bob"},
			Subject:      "Ghost message",
			Body:         "Should not exist",
		})

		// Bob's inbox should be empty
		inbox, err := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if err != nil {
			t.Fatalf("list inbox failed: %v", err)
		}
		if len(inbox.All()) != 0 {
			t.Errorf("expected 0 inbox messages, got %d", len(inbox.All()))
		}

		// Alice's sent folder should be empty
		sent, err := alice.Folder(ctx, store.FolderSent, store.ListOptions{})
		if err != nil {
			t.Fatalf("list sent failed: %v", err)
		}
		if len(sent.All()) != 0 {
			t.Errorf("expected 0 sent messages, got %d", len(sent.All()))
		}
	})
}

func TestUserResolverPartialFields(t *testing.T) {
	ctx := context.Background()

	t.Run("only non-empty fields are set", func(t *testing.T) {
		resolver := &testUserResolver{
			users: map[string]*testUser{
				"alice": {firstName: "Alice", lastName: "", email: "alice@example.com"},
			},
		}
		svc := setupServiceWithUserResolver(t, resolver)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		msg, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"alice"},
			Subject:      "Self",
			Body:         "Test",
		})
		if err != nil {
			t.Fatalf("SendMessage failed: %v", err)
		}

		meta := msg.GetMetadata()
		if meta[MetadataSenderFirstName] != "Alice" {
			t.Errorf("sender.firstname = %v, want Alice", meta[MetadataSenderFirstName])
		}
		if _, exists := meta[MetadataSenderLastName]; exists {
			t.Errorf("sender.lastname should not be set for empty last name, got %v", meta[MetadataSenderLastName])
		}
		if meta[MetadataSenderEmail] != "alice@example.com" {
			t.Errorf("sender.email = %v, want alice@example.com", meta[MetadataSenderEmail])
		}
	})

	t.Run("all fields empty sets no metadata", func(t *testing.T) {
		resolver := &testUserResolver{
			users: map[string]*testUser{
				"alice": {firstName: "", lastName: "", email: ""},
			},
		}
		svc := setupServiceWithUserResolver(t, resolver)
		defer svc.Close(ctx)

		alice := svc.Client("alice")
		msg, err := alice.SendMessage(ctx, SendRequest{
			RecipientIDs: []string{"alice"},
			Subject:      "Self",
			Body:         "Test",
		})
		if err != nil {
			t.Fatalf("SendMessage failed: %v", err)
		}

		meta := msg.GetMetadata()
		for _, key := range []string{MetadataSenderFirstName, MetadataSenderLastName, MetadataSenderEmail} {
			if _, exists := meta[key]; exists {
				t.Errorf("metadata key %q should not be set, got %v", key, meta[key])
			}
		}
	})
}

func TestNoUserResolver(t *testing.T) {
	ctx := context.Background()

	// Service without UserResolver should work as before
	svc := setupTestService(t)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	msg, err := alice.SendMessage(ctx, SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "No resolver",
		Body:         "Test",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	meta := msg.GetMetadata()
	for _, key := range []string{MetadataSenderFirstName, MetadataSenderLastName, MetadataSenderEmail} {
		if _, exists := meta[key]; exists {
			t.Errorf("metadata key %q should not be set without resolver, got %v", key, meta[key])
		}
	}
}

func TestUserResolverIsNotRetryable(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", ErrUserResolveFailed)
	if IsRetryableError(err) {
		t.Error("ErrUserResolveFailed should not be retryable")
	}
}
