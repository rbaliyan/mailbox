package mailbox

import "context"

// Recipient contains resolved information about a user.
type Recipient struct {
	// UserID is the unique user identifier.
	UserID string
	// Name is the display name of the user.
	Name string
	// Email is the user's email address (optional).
	Email string
}

// RecipientResolver maps user IDs to recipient information.
// Implementations should be safe for concurrent use.
//
// Example use cases:
//   - Populate "From" display names in inbox views
//   - Resolve email addresses for notification delivery
//   - Validate that recipient IDs are valid users
type RecipientResolver interface {
	// Resolve returns recipient information for a single user ID.
	// Returns ErrRecipientNotFound if the user ID is unknown.
	Resolve(ctx context.Context, userID string) (*Recipient, error)

	// ResolveBatch returns recipient information for multiple user IDs.
	// Returns results in the same order as input. Unknown IDs have nil entries.
	ResolveBatch(ctx context.Context, userIDs []string) ([]*Recipient, error)
}
