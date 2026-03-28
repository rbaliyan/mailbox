package crypto

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/rbaliyan/mailbox/compress"
	"github.com/rbaliyan/mailbox/store"
)

// MessageReader is the minimal interface needed for decryption.
// Satisfied by store.Message and mailbox.Message.
type MessageReader interface {
	GetBody() string
	GetHeaders() map[string]string
	GetMetadata() map[string]any
}

// IsEncrypted returns true if the message has the X-Encryption header set.
func IsEncrypted(msg MessageReader) bool {
	headers := msg.GetHeaders()
	return headers != nil && headers[HeaderEncryption] != ""
}

// Decrypt decrypts an encrypted message body for the given user.
// Returns ErrNotEncrypted if the message is not encrypted.
// Returns ErrDEKNotFound if the user has no wrapped DEK in the message.
func Decrypt(ctx context.Context, msg MessageReader, userID string, keys PrivateKeyProvider) ([]byte, error) {
	headers := msg.GetHeaders()
	if headers == nil || headers[HeaderEncryption] == "" {
		return nil, ErrNotEncrypted
	}

	meta := msg.GetMetadata()
	if meta == nil {
		return nil, ErrDEKNotFound
	}

	// Extract key type.
	keyType := X25519 // default
	if kt, ok := meta[MetaEncryptionKeyType].(string); ok {
		keyType = KeyType(kt)
	}

	// Extract wrapped DEKs map.
	deksRaw, ok := meta[MetaEncryptedDEKs]
	if !ok {
		return nil, ErrDEKNotFound
	}

	// The DEKs map may be map[string]string or map[string]any (after JSON round-trip).
	wrappedB64, err := extractUserDEK(deksRaw, userID)
	if err != nil {
		return nil, err
	}

	// Decode the wrapped DEK.
	wrappedDEK, err := base64.StdEncoding.DecodeString(wrappedB64)
	if err != nil {
		return nil, fmt.Errorf("%w: decode wrapped DEK: %v", ErrDecryptionFailed, err)
	}

	// Get private key.
	privKey, err := keys.PrivateKey(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Unwrap DEK.
	dek, err := unwrapDEK(wrappedDEK, privKey, keyType)
	if err != nil {
		return nil, err
	}

	// Decode body from base64.
	ciphertext, err := base64.StdEncoding.DecodeString(msg.GetBody())
	if err != nil {
		return nil, fmt.Errorf("%w: decode body: %v", ErrDecryptionFailed, err)
	}

	// Decrypt body.
	return decryptBody(ciphertext, dek)
}

// Open decrypts and decompresses a message body.
// This is the recommended single-call function for reading encrypted messages.
// It handles: base64 decode -> decrypt -> decompress, based on message headers.
//
// If the message is not encrypted, returns the raw body bytes.
// If the message is encrypted but not compressed, just decrypts.
func Open(ctx context.Context, msg MessageReader, userID string, keys PrivateKeyProvider) ([]byte, error) {
	var data []byte

	if IsEncrypted(msg) {
		decrypted, err := Decrypt(ctx, msg, userID, keys)
		if err != nil {
			return nil, err
		}
		data = decrypted
	} else {
		data = []byte(msg.GetBody())
	}

	// Decompress if Content-Encoding is set.
	// Both encrypted and unencrypted compressed bodies are base64-encoded
	// (the compression plugin always base64-encodes since body is a string field).
	headers := msg.GetHeaders()
	if headers != nil {
		if encoding := headers[store.HeaderContentEncoding]; encoding != "" {
			decoded, err := base64.StdEncoding.DecodeString(string(data))
			if err != nil {
				return nil, fmt.Errorf("crypto: decode compressed body: %w", err)
			}
			decompressed, err := compress.Decompress(decoded, encoding)
			if err != nil {
				return nil, err
			}
			return decompressed, nil
		}
	}

	return data, nil
}

// extractUserDEK extracts a user's wrapped DEK from the metadata map.
// Handles both map[string]string (direct) and map[string]any (after JSON deserialization).
func extractUserDEK(deksRaw any, userID string) (string, error) {
	switch deks := deksRaw.(type) {
	case map[string]string:
		b64, ok := deks[userID]
		if !ok {
			return "", fmt.Errorf("%w: %s", ErrDEKNotFound, userID)
		}
		return b64, nil
	case map[string]any:
		v, ok := deks[userID]
		if !ok {
			return "", fmt.Errorf("%w: %s", ErrDEKNotFound, userID)
		}
		b64, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%w: invalid DEK type for %s", ErrDEKNotFound, userID)
		}
		return b64, nil
	default:
		return "", fmt.Errorf("%w: unexpected DEK map type %T", ErrDEKNotFound, deksRaw)
	}
}
