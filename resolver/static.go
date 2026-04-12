// Package resolver provides RecipientResolver implementations.
package resolver

import (
	"context"
	"fmt"

	"github.com/rbaliyan/mailbox"
)

// Static is a map-based RecipientResolver for testing and simple deployments.
// It resolves user IDs from an in-memory map. Safe for concurrent use (read-only after creation).
type Static struct {
	recipients map[string]*mailbox.Recipient
}

// NewStatic creates a Static resolver from a map of user ID to Recipient.
// The map is copied to prevent external mutation.
func NewStatic(recipients map[string]*mailbox.Recipient) *Static {
	m := make(map[string]*mailbox.Recipient, len(recipients))
	for k, v := range recipients {
		m[k] = v
	}
	return &Static{recipients: m}
}

// Resolve returns recipient information for a single user ID.
func (s *Static) Resolve(_ context.Context, userID string) (*mailbox.Recipient, error) {
	r, ok := s.recipients[userID]
	if !ok {
		return nil, fmt.Errorf("recipient not found: %s", userID)
	}
	return r, nil
}

// ResolveBatch returns recipient information for multiple user IDs.
// Unknown IDs have nil entries in the returned slice.
func (s *Static) ResolveBatch(_ context.Context, userIDs []string) ([]*mailbox.Recipient, error) {
	result := make([]*mailbox.Recipient, len(userIDs))
	for i, id := range userIDs {
		result[i] = s.recipients[id]
	}
	return result, nil
}

// staticUser is a simple User implementation backed by plain fields.
type staticUser struct {
	firstName string
	lastName  string
	email     string
}

func (u *staticUser) FirstName() string { return u.firstName }
func (u *staticUser) LastName() string  { return u.lastName }
func (u *staticUser) Email() string     { return u.email }

// UserEntry holds user identity fields for StaticUserResolver.
type UserEntry struct {
	FirstName string
	LastName  string
	Email     string
}

// StaticUserResolver is a map-based UserResolver for testing and simple deployments.
// It resolves user IDs from an in-memory map. Safe for concurrent use (read-only after creation).
type StaticUserResolver struct {
	users map[string]*UserEntry
}

// NewStaticUserResolver creates a StaticUserResolver from a map of user ID to UserEntry.
// The map is copied to prevent external mutation.
func NewStaticUserResolver(users map[string]*UserEntry) *StaticUserResolver {
	m := make(map[string]*UserEntry, len(users))
	for k, v := range users {
		m[k] = v
	}
	return &StaticUserResolver{users: m}
}

// ResolveUser returns user identity information for a single user ID.
func (s *StaticUserResolver) ResolveUser(_ context.Context, userID string) (mailbox.User, error) {
	entry, ok := s.users[userID]
	if !ok {
		return nil, fmt.Errorf("user not found: %s", userID)
	}
	return &staticUser{
		firstName: entry.FirstName,
		lastName:  entry.LastName,
		email:     entry.Email,
	}, nil
}
