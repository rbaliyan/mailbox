package crypto

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// fuzzMsg is a minimal crypto.MessageReader for driving the decrypt path with
// arbitrary header/body/metadata content.
type fuzzMsg struct {
	body     string
	headers  map[string]string
	metadata map[string]any
}

func (m fuzzMsg) GetBody() string               { return m.body }
func (m fuzzMsg) GetHeaders() map[string]string { return m.headers }
func (m fuzzMsg) GetMetadata() map[string]any   { return m.metadata }

var _ MessageReader = fuzzMsg{}

// newFuzzKeypair derives an X25519 keypair from a 32-byte seed without any
// I/O. Used to pre-generate a fixed key outside f.Fuzz.
func newFuzzKeypair(t testing.TB, seed []byte) (pub, priv []byte) {
	t.Helper()
	priv = make([]byte, 32)
	copy(priv, seed)
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	return pub, priv
}

// FuzzDecryptMalformed feeds random bytes into the full decrypt path
// (Decrypt -> unwrapDEK -> decryptBody) with a fixed pre-generated key.
//
// Oracle: Decrypt must NEVER panic and must NEVER return a plaintext together
// with a nil error for attacker-controlled input. The only acceptable outcome
// for malformed input is (nil/empty, error). A non-nil plaintext with nil error
// would mean unauthenticated data was accepted as a valid message.
func FuzzDecryptMalformed(f *testing.F) {
	// A few representative malformed shapes.
	f.Add([]byte("not base64 at all"), []byte("AAAA"))
	f.Add([]byte(""), []byte(""))
	f.Add([]byte(base64.StdEncoding.EncodeToString([]byte("short"))), []byte("AAAA"))
	f.Add([]byte(base64.StdEncoding.EncodeToString(make([]byte, 200))), []byte(base64.StdEncoding.EncodeToString(make([]byte, 80))))

	const userID = "fuzz-user"
	// Fixed key generated once, outside the fuzz loop (I/O-free derivation).
	keys := NewStaticKeyResolver()
	pub, priv := newFuzzKeypair(f, []byte("deterministic-fuzz-seed-32-bytes!!"))
	keys.AddUser(userID, pub, priv)

	f.Fuzz(func(t *testing.T, body, wrappedDEK []byte) {
		msg := fuzzMsg{
			body: string(body),
			headers: map[string]string{
				HeaderEncryption: AlgoAES256GCM,
			},
			metadata: map[string]any{
				MetaEncryptionKeyType: string(X25519),
				MetaEncryptedDEKs: map[string]string{
					userID: string(wrappedDEK),
				},
			},
		}

		plaintext, err := Decrypt(context.Background(), msg, userID, keys)
		if err == nil && plaintext != nil {
			// Random body/DEK should never authenticate. If it does, AES-GCM
			// or the DEK unwrap accepted forged input.
			t.Fatalf("Decrypt accepted malformed input: body=%q wrappedDEK=%q -> plaintext=%q",
				body, wrappedDEK, plaintext)
		}
	})
}

// FuzzSealOpenRoundTrip asserts the X25519 envelope round-trips: a body sealed
// with a fresh per-message DEK wrapped to the recipient's X25519 public key
// must decrypt back to exactly the original plaintext. This exercises
// encryptBody/wrapDEK on the way in and decryptBody/unwrapDEK on the way out.
func FuzzSealOpenRoundTrip(f *testing.F) {
	f.Add([]byte("hello world"))
	f.Add([]byte(""))
	f.Add([]byte{0x00, 0x01, 0xff, 0xfe})
	f.Add([]byte("a much longer body that spans multiple AES-GCM blocks ............"))

	const userID = "recipient"
	pub, priv := newFuzzKeypair(f, []byte("seal-open-fuzz-seed-32bytes-long!!!!"))

	f.Fuzz(func(t *testing.T, plaintext []byte) {
		// Seal: fresh DEK, encrypt body, wrap DEK to recipient public key.
		dek := make([]byte, dekSize)
		if _, err := rand.Read(dek); err != nil {
			t.Fatalf("generate DEK: %v", err)
		}
		ciphertext, err := encryptBody(plaintext, dek)
		if err != nil {
			t.Fatalf("encryptBody: %v", err)
		}
		wrapped, err := wrapDEKX25519(dek, pub)
		if err != nil {
			t.Fatalf("wrapDEKX25519: %v", err)
		}

		// Assemble a message that Decrypt can read.
		msg := fuzzMsg{
			body: base64.StdEncoding.EncodeToString(ciphertext),
			headers: map[string]string{
				HeaderEncryption: AlgoAES256GCM,
			},
			metadata: map[string]any{
				MetaEncryptionKeyType: string(X25519),
				MetaEncryptedDEKs: map[string]string{
					userID: base64.StdEncoding.EncodeToString(wrapped),
				},
			},
		}

		keys := NewStaticKeyResolver()
		keys.AddUser(userID, pub, priv)

		got, err := Decrypt(context.Background(), msg, userID, keys)
		if err != nil {
			t.Fatalf("Decrypt of sealed body failed: %v", err)
		}
		if len(plaintext) == 0 && len(got) == 0 {
			return
		}
		if string(got) != string(plaintext) {
			t.Fatalf("seal/open mismatch: in=%x out=%x", plaintext, got)
		}
	})
}
