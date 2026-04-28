// Package resolver provides RecipientResolver and UserResolver implementations.
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

// staticUser is a User implementation backed by plain fields.
type staticUser struct {
	id           string
	firstName    string
	lastName     string
	email        string
	userType     string
	publicKey    string
	capabilities map[string]string
}

func (u *staticUser) ID() string                      { return u.id }
func (u *staticUser) FirstName() string               { return u.firstName }
func (u *staticUser) LastName() string                { return u.lastName }
func (u *staticUser) Email() string                   { return u.email }
func (u *staticUser) Type() string                    { return u.userType }
func (u *staticUser) PublicKey() string               { return u.publicKey }
func (u *staticUser) Capabilities() map[string]string { return u.capabilities }

// UserEntry holds user identity and capability fields for StaticUserResolver.
type UserEntry struct {
	FirstName    string
	LastName     string
	Email        string
	Type         string            // "human" | "agent" | "service" | ""
	PublicKey    string            // Ed25519 base64-encoded; empty for humans
	Capabilities map[string]string // skills, endpoint, region, model, …
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

// ResolveUser returns the full User principal for a given user ID.
func (s *StaticUserResolver) ResolveUser(_ context.Context, userID string) (mailbox.User, error) {
	entry, ok := s.users[userID]
	if !ok {
		return nil, fmt.Errorf("user not found: %s", userID)
	}
	caps := make(map[string]string, len(entry.Capabilities))
	for k, v := range entry.Capabilities {
		caps[k] = v
	}
	return &staticUser{
		id:           userID,
		firstName:    entry.FirstName,
		lastName:     entry.LastName,
		email:        entry.Email,
		userType:     entry.Type,
		publicKey:    entry.PublicKey,
		capabilities: caps,
	}, nil
}
