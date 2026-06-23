package resolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/resolver"
)

func fixtureRecipients() map[string]*mailbox.Recipient {
	return map[string]*mailbox.Recipient{
		"alice": {UserID: "alice", Name: "Alice Example", Email: "alice@example.com"},
		"bob":   {UserID: "bob", Name: "Bob Example", Email: "bob@example.com"},
	}
}

func TestStatic_Resolve(t *testing.T) {
	ctx := context.Background()
	s := resolver.NewStatic(fixtureRecipients())

	tests := []struct {
		name      string
		userID    string
		wantFound bool
		wantName  string
		wantEmail string
	}{
		{"hit alice", "alice", true, "Alice Example", "alice@example.com"},
		{"hit bob", "bob", true, "Bob Example", "bob@example.com"},
		{"miss", "carol", false, "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := s.Resolve(ctx, tc.userID)
			if tc.wantFound {
				if err != nil {
					t.Fatalf("Resolve(%q) error: %v", tc.userID, err)
				}
				if r.UserID != tc.userID {
					t.Errorf("UserID = %q, want %q", r.UserID, tc.userID)
				}
				if r.Name != tc.wantName {
					t.Errorf("Name = %q, want %q", r.Name, tc.wantName)
				}
				if r.Email != tc.wantEmail {
					t.Errorf("Email = %q, want %q", r.Email, tc.wantEmail)
				}
				return
			}
			if err == nil {
				t.Fatalf("Resolve(%q) = %+v, want error", tc.userID, r)
			}
			if r != nil {
				t.Errorf("Resolve miss returned %+v, want nil recipient", r)
			}
		})
	}
}

// TestStatic_Resolve_MissError verifies the RecipientResolver contract: Resolve
// returns (a value wrapping) mailbox.ErrRecipientNotFound when the user ID is
// unknown, so callers can detect a miss with errors.Is.
func TestStatic_Resolve_MissError(t *testing.T) {
	s := resolver.NewStatic(fixtureRecipients())

	_, err := s.Resolve(context.Background(), "carol")
	if err == nil {
		t.Fatal("expected an error for an unknown user")
	}
	if !errors.Is(err, mailbox.ErrRecipientNotFound) {
		t.Errorf("error %v does not match mailbox.ErrRecipientNotFound", err)
	}
}

func TestStatic_ResolveBatch(t *testing.T) {
	s := resolver.NewStatic(fixtureRecipients())

	ids := []string{"alice", "carol", "bob"}
	got, err := s.ResolveBatch(context.Background(), ids)
	if err != nil {
		t.Fatalf("ResolveBatch: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("ResolveBatch len = %d, want %d", len(got), len(ids))
	}
	if got[0] == nil || got[0].UserID != "alice" {
		t.Errorf("got[0] = %+v, want alice", got[0])
	}
	// Unknown IDs have nil entries, preserving input order.
	if got[1] != nil {
		t.Errorf("got[1] = %+v, want nil for unknown user", got[1])
	}
	if got[2] == nil || got[2].UserID != "bob" {
		t.Errorf("got[2] = %+v, want bob", got[2])
	}
}

// TestStatic_CopiesInput verifies that NewStatic copies the input map so that
// later external mutation cannot affect resolution.
func TestStatic_CopiesInput(t *testing.T) {
	src := fixtureRecipients()
	s := resolver.NewStatic(src)

	delete(src, "alice")
	src["mallory"] = &mailbox.Recipient{UserID: "mallory"}

	if _, err := s.Resolve(context.Background(), "alice"); err != nil {
		t.Errorf("alice should still resolve after external delete: %v", err)
	}
	if _, err := s.Resolve(context.Background(), "mallory"); err == nil {
		t.Error("mallory should not resolve; external insert must not leak in")
	}
}

// TestStatic_UserResolver_Accessors exercises the User accessors not covered by
// the existing internal test (ID, Type, PublicKey, Capabilities) and verifies
// the returned capability map is isolated from later mutation.
func TestStatic_UserResolver_Accessors(t *testing.T) {
	ctx := context.Background()
	s := resolver.NewStaticUserResolver(map[string]*resolver.UserEntry{
		"agent-1": {
			FirstName:    "Ada",
			LastName:     "Agent",
			Email:        "ada@example.com",
			Type:         "agent",
			PublicKey:    "pubkey-base64",
			Capabilities: map[string]string{"region": "us", "model": "x"},
		},
	})

	u, err := s.ResolveUser(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if u.ID() != "agent-1" {
		t.Errorf("ID() = %q, want agent-1", u.ID())
	}
	if u.Type() != "agent" {
		t.Errorf("Type() = %q, want agent", u.Type())
	}
	if u.PublicKey() != "pubkey-base64" {
		t.Errorf("PublicKey() = %q", u.PublicKey())
	}
	if u.Capabilities()["region"] != "us" || u.Capabilities()["model"] != "x" {
		t.Errorf("Capabilities() = %v", u.Capabilities())
	}

	// Mutating the returned map must not affect a fresh resolution.
	u.Capabilities()["region"] = "mutated"
	u2, _ := s.ResolveUser(ctx, "agent-1")
	if u2.Capabilities()["region"] != "us" {
		t.Errorf("capability map not isolated: got %q", u2.Capabilities()["region"])
	}
}
