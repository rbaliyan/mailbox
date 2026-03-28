package crypto

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/rbaliyan/mailbox/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// EncryptionPlugin encrypts message bodies using envelope encryption.
// Each message gets a random AES-256-GCM DEK, which is wrapped with each
// recipient's public key. Register after CompressionPlugin for
// compress-then-encrypt ordering.
type EncryptionPlugin struct {
	keys KeyResolver
	opts *encryptionOptions
}

// NewEncryptionPlugin creates an encryption plugin.
// The KeyResolver provides public keys for recipients. Options control the
// asymmetric algorithm (default X25519).
func NewEncryptionPlugin(keys KeyResolver, opts ...EncryptionOption) *EncryptionPlugin {
	o := defaultEncryptionOptions()
	for _, opt := range opts {
		opt(o)
	}
	return &EncryptionPlugin{keys: keys, opts: o}
}

func (p *EncryptionPlugin) Name() string                 { return "encryption" }
func (p *EncryptionPlugin) Init(_ context.Context) error { return nil }
func (p *EncryptionPlugin) Close(_ context.Context) error { return nil }
func (p *EncryptionPlugin) AfterSend(_ context.Context, _ string, _ store.Message) error {
	return nil
}

// BeforeSend encrypts the message body and stores per-recipient wrapped DEKs in metadata.
func (p *EncryptionPlugin) BeforeSend(ctx context.Context, userID string, draft store.DraftMessage) error {
	ctx, span := otel.Tracer("mailbox.crypto").Start(ctx, "encrypt",
		trace.WithAttributes(
			attribute.String("key_type", string(p.opts.keyType)),
			attribute.Int("recipient_count", len(draft.GetRecipientIDs())),
		),
	)
	defer span.End()

	body := draft.GetBody()
	if len(body) == 0 {
		return nil
	}

	// Generate random DEK.
	dek, err := generateDEK()
	if err != nil {
		return err
	}

	// Encrypt body with DEK.
	ciphertext, err := encryptBody([]byte(body), dek)
	if err != nil {
		return err
	}

	// Collect all user IDs that need wrapped DEKs: recipients + sender.
	recipientIDs := draft.GetRecipientIDs()
	allUserIDs := make([]string, 0, len(recipientIDs)+1)
	allUserIDs = append(allUserIDs, userID) // sender first
	allUserIDs = append(allUserIDs, recipientIDs...)

	// Resolve public keys.
	pubKeys, err := p.keys.PublicKeys(ctx, allUserIDs)
	if err != nil {
		return fmt.Errorf("crypto: resolve public keys: %w", err)
	}

	// Wrap DEK for each user.
	wrappedDEKs := make(map[string]string, len(pubKeys))
	for uid, pubKey := range pubKeys {
		wrapped, err := wrapDEK(dek, pubKey, p.opts.keyType)
		if err != nil {
			return fmt.Errorf("crypto: wrap DEK for %s: %w", uid, err)
		}
		wrappedDEKs[uid] = base64.StdEncoding.EncodeToString(wrapped)
	}

	// Set encrypted body (base64-encoded).
	draft.SetBody(base64.StdEncoding.EncodeToString(ciphertext))

	// Store wrapped DEKs and encryption metadata.
	draft.SetMetadata(MetaEncryptedDEKs, wrappedDEKs)
	draft.SetMetadata(MetaEncryptionKeyType, string(p.opts.keyType))
	draft.SetHeader(HeaderEncryption, AlgoAES256GCM)

	return nil
}
