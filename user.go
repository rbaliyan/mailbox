package mailbox

import "context"

// Metadata keys populated by UserResolver during message delivery.
// When a UserResolver is configured, these keys are set on message
// metadata with the sender's identity before validation and delivery.
const (
	MetadataSenderFirstName = "sender.firstname"
	MetadataSenderLastName  = "sender.lastname"
	MetadataSenderEmail     = "sender.email"
)

// User provides identity information about a user.
// Implementations should be safe for concurrent use.
type User interface {
	FirstName() string
	LastName() string
	Email() string
}

// UserResolver resolves user IDs to identity information.
// When configured via WithUserResolver, the service calls ResolveUser
// during message delivery to populate sender metadata. If resolution
// fails, the send operation is aborted.
//
// Implementations should be safe for concurrent use.
type UserResolver interface {
	ResolveUser(ctx context.Context, userID string) (User, error)
}
