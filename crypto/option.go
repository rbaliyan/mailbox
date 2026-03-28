package crypto

// EncryptionOption configures the EncryptionPlugin.
type EncryptionOption func(*encryptionOptions)

type encryptionOptions struct {
	keyType KeyType
}

func defaultEncryptionOptions() *encryptionOptions {
	return &encryptionOptions{
		keyType: X25519,
	}
}

// WithKeyType sets the asymmetric key type for DEK wrapping.
// Default is X25519.
func WithKeyType(kt KeyType) EncryptionOption {
	return func(o *encryptionOptions) {
		o.keyType = kt
	}
}
