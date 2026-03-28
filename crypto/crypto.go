// Package crypto provides optional E2E encryption and compression for mailbox messages.
//
// Encryption and compression are implemented as mailbox plugins (SendHook) that
// transform the message body during send. Decryption is a client-side operation
// performed after retrieving a message.
//
// # Architecture
//
// The processing pipeline on send is:
//
//	plaintext -> compress -> encrypt -> base64 -> store
//
// On read:
//
//	body -> base64-decode -> decrypt -> decompress -> plaintext
//
// Plugins are executed in registration order, so register compression before encryption:
//
//	svc, _ := mailbox.NewService(
//	    mailbox.WithStore(store),
//	    mailbox.WithPlugins(
//	        crypto.NewCompressionPlugin(crypto.Gzip),
//	        crypto.NewEncryptionPlugin(keys, crypto.WithKeyType(crypto.X25519)),
//	    ),
//	)
//
// # Encryption
//
// Each message is encrypted with a random AES-256-GCM data encryption key (DEK).
// The DEK is then wrapped (encrypted) with each recipient's public key so that
// only the intended recipients can decrypt the message. The sender's DEK is also
// included so they can read their own sent messages.
//
// Wrapped DEKs are stored in message metadata, not headers. The subject is NOT
// encrypted to preserve searchability.
//
// # Compression
//
// Message bodies are compressed before encryption using gzip or zstd. The
// Content-Encoding header indicates the algorithm used.
//
// # Decryption
//
// After retrieving a message, call Open to decrypt and decompress:
//
//	msg, _ := mb.Get(ctx, msgID)
//	plaintext, err := crypto.Open(ctx, msg, "bob", privateKeyProvider)
package crypto

import "errors"

// Header and metadata keys used by the encryption and compression plugins.
const (
	// HeaderEncryption is set on encrypted messages to indicate the body cipher.
	HeaderEncryption = "X-Encryption"

	// MetaEncryptedDEKs is the metadata key storing per-recipient wrapped DEKs.
	// Value is map[string]string where key is userID and value is base64-encoded wrapped DEK.
	MetaEncryptedDEKs = "x-encrypted-deks"

	// MetaEncryptionKeyType is the metadata key indicating the asymmetric algorithm
	// used for DEK wrapping (e.g., "x25519", "rsa-oaep").
	MetaEncryptionKeyType = "x-encryption-key-type"
)

// Algorithm identifiers.
const (
	AlgoAES256GCM = "aes-256-gcm"
)

// KeyType identifies the asymmetric algorithm used for DEK wrapping.
type KeyType string

const (
	// X25519 uses X25519 Diffie-Hellman key agreement with AES-256-GCM for DEK wrapping.
	// Compact keys (32 bytes) and wrapped DEKs (~80 bytes). Recommended for most use cases.
	X25519 KeyType = "x25519"

	// RSAOAEP uses RSA-OAEP with SHA-256 for DEK wrapping.
	// Larger keys and wrapped DEKs (~256 bytes for RSA-2048). Compatible with legacy PKI.
	RSAOAEP KeyType = "rsa-oaep"
)

// Sentinel errors.
var (
	// ErrNotEncrypted is returned when attempting to decrypt a message that is not encrypted.
	ErrNotEncrypted = errors.New("crypto: message is not encrypted")

	// ErrDEKNotFound is returned when the user's wrapped DEK is not in the message metadata.
	ErrDEKNotFound = errors.New("crypto: no DEK found for user")

	// ErrDecryptionFailed is returned when decryption fails (wrong key, tampered data).
	ErrDecryptionFailed = errors.New("crypto: decryption failed")

	// ErrKeyNotFound is returned when a user's public or private key cannot be resolved.
	ErrKeyNotFound = errors.New("crypto: key not found")

	// ErrUnsupportedKeyType is returned for unknown key types.
	ErrUnsupportedKeyType = errors.New("crypto: unsupported key type")

)
