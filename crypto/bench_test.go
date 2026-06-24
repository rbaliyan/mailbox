package crypto

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"testing"

	"github.com/rbaliyan/mailbox/store/memory"
	"golang.org/x/crypto/curve25519"
)

// benchSink defends round-trip results from dead-code elimination.
var benchSink any

// x25519Keypair generates an X25519 keypair (32-byte public, 32-byte private).
// It mirrors mailboxtest.X25519Keypair but takes *testing.B.
func x25519Keypair(b *testing.B) (pub, priv []byte) {
	b.Helper()
	priv = make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		b.Fatalf("generate private key: %v", err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		b.Fatalf("derive public key: %v", err)
	}
	return pub, priv
}

// rsaKeypair generates an RSA-2048 keypair encoded as the DER forms the
// envelope wrapper expects: PKIX for the public key, PKCS8 for the private key.
func rsaKeypair(b *testing.B) (pub, priv []byte) {
	b.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		b.Fatalf("generate RSA key: %v", err)
	}
	pub, err = x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		b.Fatalf("marshal RSA public key: %v", err)
	}
	priv, err = x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		b.Fatalf("marshal RSA private key: %v", err)
	}
	return pub, priv
}

// BenchmarkSealOpen measures the full encryption seal (plugin BeforeSend:
// AES-256-GCM body encryption + per-recipient DEK wrapping) plus the Open
// round-trip (DEK unwrap + body decrypt) for each asymmetric key type.
//
// Keys are pre-generated outside b.Loop() so the benchmark measures the
// per-message envelope work, not key generation.
func BenchmarkSealOpen(b *testing.B) {
	const (
		sender    = "alice"
		recipient = "bob"
	)
	body := make([]byte, 4*1024)
	if _, err := rand.Read(body); err != nil {
		b.Fatalf("body: %v", err)
	}

	cases := []struct {
		name    string
		keyType KeyType
		genKeys func(*testing.B) (pub, priv []byte)
	}{
		{"x25519", X25519, x25519Keypair},
		{"rsa", RSAOAEP, rsaKeypair},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			// Pre-generate keys for sender and recipient (outside b.Loop).
			senderPub, senderPriv := c.genKeys(b)
			recipientPub, recipientPriv := c.genKeys(b)

			keys := NewStaticKeyResolver()
			keys.AddUser(sender, senderPub, senderPriv)
			keys.AddUser(recipient, recipientPub, recipientPriv)

			plugin := NewEncryptionPlugin(keys, WithKeyType(c.keyType))
			st := memory.New()
			ctx := context.Background()

			b.ReportAllocs()
			for b.Loop() {
				// Seal: a fresh draft is encrypted in place by the plugin.
				draft := st.NewDraft(sender)
				draft.SetRecipients(recipient).SetBody(string(body))
				if err := plugin.BeforeSend(ctx, sender, draft); err != nil {
					b.Fatal(err)
				}

				// Open: the recipient decrypts their copy.
				plaintext, err := Open(ctx, draft, recipient, keys)
				if err != nil {
					b.Fatal(err)
				}
				benchSink = plaintext
			}
		})
	}
}
