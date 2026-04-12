package resolver

import (
	"context"
	"testing"
)

func TestStaticUserResolver(t *testing.T) {
	users := map[string]*UserEntry{
		"alice": {FirstName: "Alice", LastName: "Smith", Email: "alice@example.com"},
		"bob":   {FirstName: "Bob", LastName: "Jones", Email: "bob@example.com"},
	}
	resolver := NewStaticUserResolver(users)
	ctx := context.Background()

	t.Run("resolves known user", func(t *testing.T) {
		user, err := resolver.ResolveUser(ctx, "alice")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user.FirstName() != "Alice" {
			t.Errorf("FirstName() = %q, want Alice", user.FirstName())
		}
		if user.LastName() != "Smith" {
			t.Errorf("LastName() = %q, want Smith", user.LastName())
		}
		if user.Email() != "alice@example.com" {
			t.Errorf("Email() = %q, want alice@example.com", user.Email())
		}
	})

	t.Run("resolves second user", func(t *testing.T) {
		user, err := resolver.ResolveUser(ctx, "bob")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user.FirstName() != "Bob" {
			t.Errorf("FirstName() = %q, want Bob", user.FirstName())
		}
		if user.LastName() != "Jones" {
			t.Errorf("LastName() = %q, want Jones", user.LastName())
		}
		if user.Email() != "bob@example.com" {
			t.Errorf("Email() = %q, want bob@example.com", user.Email())
		}
	})

	t.Run("returns error for unknown user", func(t *testing.T) {
		_, err := resolver.ResolveUser(ctx, "unknown")
		if err == nil {
			t.Fatal("expected error for unknown user, got nil")
		}
	})

	t.Run("map is copied defensively", func(t *testing.T) {
		original := map[string]*UserEntry{
			"alice": {FirstName: "Alice", LastName: "A", Email: "a@test.com"},
		}
		r := NewStaticUserResolver(original)

		// Mutate the original map
		original["bob"] = &UserEntry{FirstName: "Bob", LastName: "B", Email: "b@test.com"}

		// Bob should not be resolvable
		_, err := r.ResolveUser(ctx, "bob")
		if err == nil {
			t.Error("expected error after mutating original map, got nil")
		}
	})

	t.Run("handles partial fields", func(t *testing.T) {
		r := NewStaticUserResolver(map[string]*UserEntry{
			"partial": {FirstName: "Only", LastName: "", Email: ""},
		})
		user, err := r.ResolveUser(ctx, "partial")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user.FirstName() != "Only" {
			t.Errorf("FirstName() = %q, want Only", user.FirstName())
		}
		if user.LastName() != "" {
			t.Errorf("LastName() = %q, want empty", user.LastName())
		}
		if user.Email() != "" {
			t.Errorf("Email() = %q, want empty", user.Email())
		}
	})

	t.Run("empty resolver returns error for any user", func(t *testing.T) {
		r := NewStaticUserResolver(nil)
		_, err := r.ResolveUser(ctx, "anyone")
		if err == nil {
			t.Fatal("expected error from empty resolver, got nil")
		}
	})
}
