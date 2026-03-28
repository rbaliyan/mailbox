package crypto

import (
	"context"
	"fmt"
	"sync"
)

// KeyResolver provides public keys for message recipients.
// Implementations must be safe for concurrent use.
type KeyResolver interface {
	// PublicKey returns the public key for a single user.
	// Returns ErrKeyNotFound if the user has no registered key.
	PublicKey(ctx context.Context, userID string) ([]byte, error)

	// PublicKeys returns public keys for multiple users.
	// The returned map contains only users with available keys.
	// Users without keys are silently omitted (not an error).
	PublicKeys(ctx context.Context, userIDs []string) (map[string][]byte, error)
}

// PrivateKeyProvider provides private keys for message decryption.
// Implementations must be safe for concurrent use.
type PrivateKeyProvider interface {
	// PrivateKey returns the private key for a user.
	// Returns ErrKeyNotFound if the user has no registered key.
	PrivateKey(ctx context.Context, userID string) ([]byte, error)
}

// StaticKeyResolver is an in-memory key resolver for testing and simple deployments.
// It implements both KeyResolver and PrivateKeyProvider.
type StaticKeyResolver struct {
	mu          sync.RWMutex
	publicKeys  map[string][]byte
	privateKeys map[string][]byte
}

// Compile-time interface checks.
var (
	_ KeyResolver       = (*StaticKeyResolver)(nil)
	_ PrivateKeyProvider = (*StaticKeyResolver)(nil)
)

// NewStaticKeyResolver creates an empty static key resolver.
func NewStaticKeyResolver() *StaticKeyResolver {
	return &StaticKeyResolver{
		publicKeys:  make(map[string][]byte),
		privateKeys: make(map[string][]byte),
	}
}

// AddUser registers a public and private key pair for a user.
func (r *StaticKeyResolver) AddUser(userID string, publicKey, privateKey []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publicKeys[userID] = publicKey
	r.privateKeys[userID] = privateKey
}

// PublicKey returns the public key for a user.
func (r *StaticKeyResolver) PublicKey(_ context.Context, userID string) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.publicKeys[userID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, userID)
	}
	return key, nil
}

// PublicKeys returns public keys for multiple users.
func (r *StaticKeyResolver) PublicKeys(_ context.Context, userIDs []string) (map[string][]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string][]byte, len(userIDs))
	for _, uid := range userIDs {
		if key, ok := r.publicKeys[uid]; ok {
			result[uid] = key
		}
	}
	return result, nil
}

// PrivateKey returns the private key for a user.
func (r *StaticKeyResolver) PrivateKey(_ context.Context, userID string) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.privateKeys[userID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, userID)
	}
	return key, nil
}
