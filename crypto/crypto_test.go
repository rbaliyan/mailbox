package crypto_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"testing"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/compress"
	"github.com/rbaliyan/mailbox/crypto"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
	"golang.org/x/crypto/curve25519"
)

// generateX25519Keypair generates a random X25519 keypair.
func generateX25519Keypair(t *testing.T) (pub, priv []byte) {
	t.Helper()
	priv = make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// generateRSAKeypair generates a random RSA-2048 keypair in DER format.
func generateRSAKeypair(t *testing.T) (pubDER, privDER []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err = x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	privDER, err = x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pubDER, privDER
}

func newTestService(t *testing.T, plugins ...mailbox.Plugin) mailbox.Service {
	t.Helper()
	opts := []mailbox.Option{mailbox.WithStore(memory.New())}
	if len(plugins) > 0 {
		opts = append(opts, mailbox.WithPlugins(plugins...))
	}
	svc, err := mailbox.New(mailbox.Config{}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return svc
}

// --- Encryption Tests ---

func TestEncryptionPlugin_X25519_RoundTrip(t *testing.T) {
	ctx := context.Background()

	alicePub, alicePriv := generateX25519Keypair(t)
	bobPub, bobPriv := generateX25519Keypair(t)

	keys := crypto.NewStaticKeyResolver()
	keys.AddUser("alice", alicePub, alicePriv)
	keys.AddUser("bob", bobPub, bobPriv)

	plugin := crypto.NewEncryptionPlugin(keys)
	svc := newTestService(t, plugin)
	defer svc.Close(ctx)

	original := "Secret message content"
	alice := svc.Client("alice")
	msg, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Encrypted",
		Body:         original,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Body should be encrypted (not plaintext).
	if msg.GetBody() == original {
		t.Error("body should be encrypted")
	}

	// X-Encryption header should be set.
	if msg.GetHeaders()[crypto.HeaderEncryption] != crypto.AlgoAES256GCM {
		t.Errorf("expected X-Encryption header")
	}

	// Subject should remain plaintext.
	if msg.GetSubject() != "Encrypted" {
		t.Errorf("subject should be plaintext, got %q", msg.GetSubject())
	}

	// Bob should be able to decrypt.
	bob := svc.Client("bob")
	received, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	plaintext, err := crypto.Open(ctx, received.All()[0], "bob", keys)
	if err != nil {
		t.Fatalf("open as bob: %v", err)
	}
	if string(plaintext) != original {
		t.Errorf("expected %q, got %q", original, string(plaintext))
	}

	// Alice should also be able to decrypt her sent copy.
	sent, _ := alice.Folder(ctx, store.FolderSent, store.ListOptions{})
	plaintext, err = crypto.Open(ctx, sent.All()[0], "alice", keys)
	if err != nil {
		t.Fatalf("open as alice: %v", err)
	}
	if string(plaintext) != original {
		t.Errorf("sender decryption failed: expected %q, got %q", original, string(plaintext))
	}
}

func TestEncryptionPlugin_RSA_RoundTrip(t *testing.T) {
	ctx := context.Background()

	alicePub, alicePriv := generateRSAKeypair(t)
	bobPub, bobPriv := generateRSAKeypair(t)

	keys := crypto.NewStaticKeyResolver()
	keys.AddUser("alice", alicePub, alicePriv)
	keys.AddUser("bob", bobPub, bobPriv)

	plugin := crypto.NewEncryptionPlugin(keys, crypto.WithKeyType(crypto.RSAOAEP))
	svc := newTestService(t, plugin)
	defer svc.Close(ctx)

	original := "RSA encrypted content"
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "RSA Test",
		Body:         original,
	})

	bob := svc.Client("bob")
	received, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	plaintext, err := crypto.Open(ctx, received.All()[0], "bob", keys)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plaintext) != original {
		t.Errorf("expected %q, got %q", original, string(plaintext))
	}
}

func TestEncryptionPlugin_MultipleRecipients(t *testing.T) {
	ctx := context.Background()

	keys := crypto.NewStaticKeyResolver()
	users := []string{"alice", "bob", "charlie", "dave"}
	for _, u := range users {
		pub, priv := generateX25519Keypair(t)
		keys.AddUser(u, pub, priv)
	}

	plugin := crypto.NewEncryptionPlugin(keys)
	svc := newTestService(t, plugin)
	defer svc.Close(ctx)

	original := "Message for everyone"
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob", "charlie", "dave"},
		Subject:      "Group",
		Body:         original,
	})

	// Each recipient should be able to decrypt.
	for _, uid := range []string{"bob", "charlie", "dave"} {
		mb := svc.Client(uid)
		inbox, _ := mb.Folder(ctx, store.FolderInbox, store.ListOptions{})
		if len(inbox.All()) != 1 {
			t.Fatalf("%s: expected 1 message, got %d", uid, len(inbox.All()))
		}
		plaintext, err := crypto.Open(ctx, inbox.All()[0], uid, keys)
		if err != nil {
			t.Fatalf("%s: open: %v", uid, err)
		}
		if string(plaintext) != original {
			t.Errorf("%s: expected %q, got %q", uid, original, string(plaintext))
		}
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	ctx := context.Background()

	alicePub, alicePriv := generateX25519Keypair(t)
	bobPub, bobPriv := generateX25519Keypair(t)
	_, evePriv := generateX25519Keypair(t)

	keys := crypto.NewStaticKeyResolver()
	keys.AddUser("alice", alicePub, alicePriv)
	keys.AddUser("bob", bobPub, bobPriv)

	plugin := crypto.NewEncryptionPlugin(keys)
	svc := newTestService(t, plugin)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Secret",
		Body:         "classified",
	})

	// Eve has bob's message but wrong private key.
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})

	eveKeys := crypto.NewStaticKeyResolver()
	eveKeys.AddUser("bob", bobPub, evePriv) // wrong private key

	_, err := crypto.Open(ctx, inbox.All()[0], "bob", eveKeys)
	if err == nil {
		t.Fatal("expected decryption to fail with wrong key")
	}
}

func TestDecrypt_NotEncrypted(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Plain",
		Body:         "not encrypted",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})

	// Open on unencrypted message should return the body as-is.
	plaintext, err := crypto.Open(ctx, inbox.All()[0], "bob", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plaintext) != "not encrypted" {
		t.Errorf("expected plaintext body, got %q", string(plaintext))
	}

	// Decrypt on unencrypted message should return ErrNotEncrypted.
	_, err = crypto.Decrypt(ctx, inbox.All()[0], "bob", nil)
	if err != crypto.ErrNotEncrypted {
		t.Errorf("expected ErrNotEncrypted, got %v", err)
	}
}

func TestIsEncrypted(t *testing.T) {
	ctx := context.Background()

	keys := crypto.NewStaticKeyResolver()
	pub, priv := generateX25519Keypair(t)
	keys.AddUser("alice", pub, priv)
	pub2, priv2 := generateX25519Keypair(t)
	keys.AddUser("bob", pub2, priv2)

	encSvc := newTestService(t, crypto.NewEncryptionPlugin(keys))
	defer encSvc.Close(ctx)

	plainSvc := newTestService(t)
	defer plainSvc.Close(ctx)

	// Encrypted message.
	encSvc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Encrypted",
		Body:         "secret",
	})
	encInbox, _ := encSvc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if !crypto.IsEncrypted(encInbox.All()[0]) {
		t.Error("should be encrypted")
	}

	// Plain message.
	plainSvc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Plain",
		Body:         "open",
	})
	plainInbox, _ := plainSvc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if crypto.IsEncrypted(plainInbox.All()[0]) {
		t.Error("should not be encrypted")
	}
}

// --- Compression + Encryption Combined ---

func TestCompressThenEncrypt_RoundTrip(t *testing.T) {
	ctx := context.Background()

	alicePub, alicePriv := generateX25519Keypair(t)
	bobPub, bobPriv := generateX25519Keypair(t)

	keys := crypto.NewStaticKeyResolver()
	keys.AddUser("alice", alicePub, alicePriv)
	keys.AddUser("bob", bobPub, bobPriv)

	// Compression first, then encryption (registration order matters).
	svc := newTestService(t,
		compress.NewPlugin(compress.Gzip),
		crypto.NewEncryptionPlugin(keys),
	)
	defer svc.Close(ctx)

	original := "This message should be compressed then encrypted. Repeating data helps compression: aaaaaaaaaaaaaaaaaaaaaaaaaaa"
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Compressed+Encrypted",
		Body:         original,
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]

	// Should have both headers.
	if !crypto.IsEncrypted(msg) {
		t.Error("should be encrypted")
	}
	if !compress.IsCompressed(msg) {
		t.Error("should be compressed")
	}

	// Open handles both decrypt + decompress.
	plaintext, err := crypto.Open(ctx, msg, "bob", keys)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plaintext) != original {
		t.Errorf("expected %q, got %q", original, string(plaintext))
	}
}

// --- Static Key Resolver Tests ---

func TestStaticKeyResolver_NotFound(t *testing.T) {
	ctx := context.Background()
	keys := crypto.NewStaticKeyResolver()

	_, err := keys.PublicKey(ctx, "unknown")
	if err == nil {
		t.Fatal("expected error for unknown user")
	}

	_, err = keys.PrivateKey(ctx, "unknown")
	if err == nil {
		t.Fatal("expected error for unknown user")
	}
}

func TestStaticKeyResolver_BatchPartial(t *testing.T) {
	ctx := context.Background()
	keys := crypto.NewStaticKeyResolver()
	keys.AddUser("alice", []byte("pub"), []byte("priv"))

	result, err := keys.PublicKeys(ctx, []string{"alice", "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 result, got %d", len(result))
	}
	if _, ok := result["alice"]; !ok {
		t.Error("expected alice in results")
	}
}

// --- Example ---

func ExampleOpen() {
	ctx := context.Background()

	// Generate keypairs.
	alicePriv := make([]byte, 32)
	rand.Read(alicePriv)
	alicePub, _ := curve25519.X25519(alicePriv, curve25519.Basepoint)
	bobPriv := make([]byte, 32)
	rand.Read(bobPriv)
	bobPub, _ := curve25519.X25519(bobPriv, curve25519.Basepoint)

	keys := crypto.NewStaticKeyResolver()
	keys.AddUser("alice", alicePub, alicePriv)
	keys.AddUser("bob", bobPub, bobPriv)

	// Create service with compress-then-encrypt plugins.
	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(
			compress.NewPlugin(compress.Gzip),
			crypto.NewEncryptionPlugin(keys),
		),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Alice sends an encrypted message.
	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Secret",
		Body:         "Hello Bob!",
	})

	// Bob reads and decrypts.
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	plaintext, _ := crypto.Open(ctx, inbox.All()[0], "bob", keys)
	fmt.Println(string(plaintext))
	// Output:
	// Hello Bob!
}

// ExampleNewEncryptionPlugin_rsa demonstrates RSA-OAEP encryption.
func ExampleNewEncryptionPlugin_rsa() {
	ctx := context.Background()

	// Generate RSA keypairs.
	aliceKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	bobKey, _ := rsa.GenerateKey(rand.Reader, 2048)

	alicePub, _ := x509.MarshalPKIXPublicKey(&aliceKey.PublicKey)
	alicePriv, _ := x509.MarshalPKCS8PrivateKey(aliceKey)
	bobPub, _ := x509.MarshalPKIXPublicKey(&bobKey.PublicKey)
	bobPriv, _ := x509.MarshalPKCS8PrivateKey(bobKey)

	keys := crypto.NewStaticKeyResolver()
	keys.AddUser("alice", alicePub, alicePriv)
	keys.AddUser("bob", bobPub, bobPriv)

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(
			crypto.NewEncryptionPlugin(keys, crypto.WithKeyType(crypto.RSAOAEP)),
		),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "RSA encrypted",
		Body:         "Classified content",
	})

	inbox, _ := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	plaintext, _ := crypto.Open(ctx, inbox.All()[0], "bob", keys)
	fmt.Println(string(plaintext))
	// Output:
	// Classified content
}

// ExampleNewEncryptionPlugin_multiRecipient demonstrates multi-recipient encryption.
func ExampleNewEncryptionPlugin_multiRecipient() {
	ctx := context.Background()

	keys := crypto.NewStaticKeyResolver()
	for _, user := range []string{"alice", "bob", "charlie"} {
		priv := make([]byte, 32)
		rand.Read(priv)
		pub, _ := curve25519.X25519(priv, curve25519.Basepoint)
		keys.AddUser(user, pub, priv)
	}

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(crypto.NewEncryptionPlugin(keys)),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Alice sends to bob and charlie. Each gets their own wrapped DEK.
	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob", "charlie"},
		Subject:      "Group secret",
		Body:         "Only recipients can read this",
	})

	// Both recipients can decrypt independently.
	for _, user := range []string{"bob", "charlie"} {
		inbox, _ := svc.Client(user).Folder(ctx, store.FolderInbox, store.ListOptions{})
		plaintext, _ := crypto.Open(ctx, inbox.All()[0], user, keys)
		fmt.Printf("%s: %s\n", user, string(plaintext))
	}
	// Output:
	// bob: Only recipients can read this
	// charlie: Only recipients can read this
}

// ExampleOpen_senderDecrypt demonstrates that senders can decrypt their own sent messages.
func ExampleOpen_senderDecrypt() {
	ctx := context.Background()

	keys := crypto.NewStaticKeyResolver()
	for _, user := range []string{"alice", "bob"} {
		priv := make([]byte, 32)
		rand.Read(priv)
		pub, _ := curve25519.X25519(priv, curve25519.Basepoint)
		keys.AddUser(user, pub, priv)
	}

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(crypto.NewEncryptionPlugin(keys)),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	alice := svc.Client("alice")
	alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "My sent message",
		Body:         "I can read this too",
	})

	// Alice decrypts her own sent copy.
	sent, _ := alice.Folder(ctx, store.FolderSent, store.ListOptions{})
	plaintext, _ := crypto.Open(ctx, sent.All()[0], "alice", keys)
	fmt.Println(string(plaintext))
	// Output:
	// I can read this too
}

// ExampleOpen_compressThenEncrypt demonstrates the full compress-then-encrypt pipeline.
func ExampleOpen_compressThenEncrypt() {
	ctx := context.Background()

	keys := crypto.NewStaticKeyResolver()
	for _, user := range []string{"alice", "bob"} {
		priv := make([]byte, 32)
		rand.Read(priv)
		pub, _ := curve25519.X25519(priv, curve25519.Basepoint)
		keys.AddUser(user, pub, priv)
	}

	// Register compression BEFORE encryption for compress-then-encrypt.
	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(
			compress.NewPlugin(compress.Gzip),     // step 1: compress
			crypto.NewEncryptionPlugin(keys),       // step 2: encrypt
		),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Compressed and encrypted",
		Body:         "This message is compressed then encrypted. Subject stays searchable.",
	})

	inbox, _ := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]

	// Subject is always plaintext (searchable).
	fmt.Println("subject:", msg.GetSubject())
	fmt.Println("encrypted:", crypto.IsEncrypted(msg))

	// Open handles decrypt then decompress automatically.
	plaintext, _ := crypto.Open(ctx, msg, "bob", keys)
	fmt.Println("body:", string(plaintext))
	// Output:
	// subject: Compressed and encrypted
	// encrypted: true
	// body: This message is compressed then encrypted. Subject stays searchable.
}

// ExampleOpen_metadata demonstrates that encryption metadata is accessible.
func ExampleOpen_metadata() {
	ctx := context.Background()

	keys := crypto.NewStaticKeyResolver()
	for _, user := range []string{"alice", "bob"} {
		priv := make([]byte, 32)
		rand.Read(priv)
		pub, _ := curve25519.X25519(priv, curve25519.Basepoint)
		keys.AddUser(user, pub, priv)
	}

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(crypto.NewEncryptionPlugin(keys)),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Send with application metadata alongside encryption metadata.
	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "With metadata",
		Body:         "secret",
		Metadata:     map[string]any{"priority": "high"},
	})

	inbox, _ := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]
	meta := msg.GetMetadata()

	// Application metadata is preserved alongside encryption metadata.
	fmt.Println("priority:", meta["priority"])
	fmt.Println("has deks:", meta[crypto.MetaEncryptedDEKs] != nil)
	fmt.Println("key type:", meta[crypto.MetaEncryptionKeyType])
	// Output:
	// priority: high
	// has deks: true
	// key type: x25519
}

func ExampleIsEncrypted() {
	ctx := context.Background()

	svc, _ := mailbox.New(mailbox.Config{}, mailbox.WithStore(memory.New()))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Plain",
		Body:         "not encrypted",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})

	fmt.Println("encrypted:", crypto.IsEncrypted(inbox.All()[0]))
	// Output:
	// encrypted: false
}

func init() {
	// Suppress noisy log output during examples.
	log.SetOutput(io.Discard)
}
