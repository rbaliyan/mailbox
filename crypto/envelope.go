package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
)

const dekSize = 32 // AES-256

// generateDEK creates a random 256-bit data encryption key.
func generateDEK() ([]byte, error) {
	dek := make([]byte, dekSize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("crypto: generate DEK: %w", err)
	}
	return dek, nil
}

// encryptBody encrypts plaintext using AES-256-GCM with the given DEK.
// Returns nonce (12 bytes) + ciphertext + tag.
func encryptBody(plaintext, dek []byte) ([]byte, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	// Seal appends ciphertext+tag after nonce
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decryptBody decrypts ciphertext produced by encryptBody.
// Input format: nonce (12 bytes) + ciphertext + tag.
func decryptBody(ciphertext, dek []byte) ([]byte, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("%w: aes cipher: %v", ErrDecryptionFailed, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm: %v", ErrDecryptionFailed, err)
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("%w: ciphertext too short", ErrDecryptionFailed)
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryptionFailed, err)
	}
	return plaintext, nil
}

// wrapDEK encrypts a DEK with the recipient's public key.
func wrapDEK(dek, publicKey []byte, keyType KeyType) ([]byte, error) {
	switch keyType {
	case X25519:
		return wrapDEKX25519(dek, publicKey)
	case RSAOAEP:
		return wrapDEKRSA(dek, publicKey)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedKeyType, keyType)
	}
}

// unwrapDEK decrypts a wrapped DEK with the recipient's private key.
func unwrapDEK(wrapped, privateKey []byte, keyType KeyType) ([]byte, error) {
	switch keyType {
	case X25519:
		return unwrapDEKX25519(wrapped, privateKey)
	case RSAOAEP:
		return unwrapDEKRSA(wrapped, privateKey)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedKeyType, keyType)
	}
}

// --- X25519 key wrapping ---
// Uses ephemeral X25519 key agreement to derive a shared secret, then
// encrypts the DEK with AES-256-GCM using the shared secret as key.
// Output: ephemeral public key (32) + nonce (12) + encrypted DEK + tag (16)

func wrapDEKX25519(dek, recipientPub []byte) ([]byte, error) {
	if len(recipientPub) != 32 {
		return nil, fmt.Errorf("%w: x25519 public key must be 32 bytes", ErrUnsupportedKeyType)
	}

	// Generate ephemeral keypair.
	ephPriv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, ephPriv); err != nil {
		return nil, err
	}
	ephPub, err := curve25519.X25519(ephPriv, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	// Derive shared secret.
	shared, err := curve25519.X25519(ephPriv, recipientPub)
	if err != nil {
		return nil, err
	}
	// Hash shared secret to get AES key (avoid reuse of raw DH output).
	key := sha256.Sum256(shared)

	// Encrypt DEK with shared secret.
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	encrypted := gcm.Seal(nil, nonce, dek, nil)

	// Output: ephPub (32) + nonce (12) + encrypted DEK + tag
	result := make([]byte, 0, 32+len(nonce)+len(encrypted))
	result = append(result, ephPub...)
	result = append(result, nonce...)
	result = append(result, encrypted...)
	return result, nil
}

func unwrapDEKX25519(wrapped, privateKey []byte) ([]byte, error) {
	if len(privateKey) != 32 {
		return nil, fmt.Errorf("%w: x25519 private key must be 32 bytes", ErrDecryptionFailed)
	}
	// Parse: ephPub (32) + nonce (12) + encrypted DEK + tag
	if len(wrapped) < 32+12+16 {
		return nil, fmt.Errorf("%w: wrapped DEK too short", ErrDecryptionFailed)
	}
	ephPub := wrapped[:32]
	nonce := wrapped[32:44]
	encrypted := wrapped[44:]

	// Derive shared secret.
	shared, err := curve25519.X25519(privateKey, ephPub)
	if err != nil {
		return nil, fmt.Errorf("%w: x25519: %v", ErrDecryptionFailed, err)
	}
	key := sha256.Sum256(shared)

	// Decrypt DEK.
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("%w: aes: %v", ErrDecryptionFailed, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm: %v", ErrDecryptionFailed, err)
	}
	dek, err := gcm.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: unwrap: %v", ErrDecryptionFailed, err)
	}
	return dek, nil
}

// --- RSA-OAEP key wrapping ---

func wrapDEKRSA(dek, publicKeyDER []byte) ([]byte, error) {
	pub, err := x509.ParsePKIXPublicKey(publicKeyDER)
	if err != nil {
		return nil, fmt.Errorf("%w: parse RSA public key: %v", ErrUnsupportedKeyType, err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: not an RSA public key", ErrUnsupportedKeyType)
	}
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPub, dek, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: rsa encrypt: %w", err)
	}
	return wrapped, nil
}

func unwrapDEKRSA(wrapped, privateKeyDER []byte) ([]byte, error) {
	priv, err := x509.ParsePKCS8PrivateKey(privateKeyDER)
	if err != nil {
		return nil, fmt.Errorf("%w: parse RSA private key: %v", ErrDecryptionFailed, err)
	}
	rsaPriv, ok := priv.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: not an RSA private key", ErrDecryptionFailed)
	}
	dek, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, rsaPriv, wrapped, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: rsa decrypt: %v", ErrDecryptionFailed, err)
	}
	return dek, nil
}
