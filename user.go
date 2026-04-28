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

// User is the full principal — identity, type, capabilities, and signing key.
// Implementations should be safe for concurrent use.
type User interface {
	// ID returns the unique identifier for this principal (typically their email address).
	ID() string
	FirstName() string
	LastName() string
	Email() string
	// Type classifies the principal: "human", "agent", "service", or "" (unset).
	Type() string
	// PublicKey returns the principal's Ed25519 public key, base64-encoded.
	// Empty for human users; set for agents and services that sign their payloads.
	PublicKey() string
	// Capabilities returns routing and capability metadata: skills, endpoint, region, model, etc.
	// Callers must not mutate the returned map.
	Capabilities() map[string]string
}

// UserResolver resolves user IDs to the full User principal.
// When configured via WithUserResolver, the service calls ResolveUser
// during message delivery to populate sender metadata. If resolution
// fails, the send operation is aborted.
//
// Implementations should be safe for concurrent use.
type UserResolver interface {
	ResolveUser(ctx context.Context, userID string) (User, error)
}
