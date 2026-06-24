package content

import (
	"bytes"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// msgReader is a store.MessageReader for feeding arbitrary bodies, content
// types, and schemas into Decode without constructing a full message. Only
// GetBody, GetHeaders, and GetMetadata carry fuzz input; the rest are zero
// values to satisfy the interface.
type msgReader struct {
	body     string
	headers  map[string]string
	metadata map[string]any
}

func (m msgReader) GetBody() string               { return m.body }
func (m msgReader) GetHeaders() map[string]string { return m.headers }
func (m msgReader) GetMetadata() map[string]any   { return m.metadata }

func (m msgReader) GetID() string                      { return "" }
func (m msgReader) GetOwnerID() string                 { return "" }
func (m msgReader) GetSenderID() string                { return "" }
func (m msgReader) GetSubject() string                 { return "" }
func (m msgReader) GetRecipientIDs() []string          { return nil }
func (m msgReader) GetAttachments() []store.Attachment { return nil }
func (m msgReader) GetCreatedAt() time.Time            { return time.Time{} }
func (m msgReader) GetUpdatedAt() time.Time            { return time.Time{} }
func (m msgReader) GetExpiresAt() *time.Time           { return nil }
func (m msgReader) GetAvailableAt() *time.Time         { return nil }
func (m msgReader) GetThreadID() string                { return "" }
func (m msgReader) GetReplyToID() string               { return "" }
func (m msgReader) GetExternalID() string              { return "" }

var _ store.MessageReader = msgReader{}

// FuzzBinaryCodecRoundTrip asserts the binary (base64) codec is a faithful
// round-trip: Decode(Encode(b)) must equal b for every input. A mismatch would
// mean binary message bodies (protobuf, msgpack, octet-stream) are silently
// corrupted in storage.
func FuzzBinaryCodecRoundTrip(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte{})
	f.Add([]byte("hello"))
	f.Add([]byte{0x00, 0xff, 0xfe, 0x01, 0x80})
	f.Add([]byte("application/protobuf\x00binary\x01\x02"))
	f.Add(bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 64))

	codec := OctetStream // binaryCodec, base64.StdEncoding

	f.Fuzz(func(t *testing.T, data []byte) {
		encoded, err := codec.Encode(data)
		if err != nil {
			t.Fatalf("Encode returned error for %d bytes: %v", len(data), err)
		}

		decoded, err := codec.Decode(encoded)
		if err != nil {
			t.Fatalf("Decode failed for self-produced encoding %q: %v", encoded, err)
		}

		// Normalize nil vs empty: both represent zero bytes.
		if len(data) == 0 && len(decoded) == 0 {
			return
		}
		if !bytes.Equal(decoded, data) {
			t.Fatalf("round-trip mismatch: input %x -> encoded %q -> decoded %x", data, encoded, decoded)
		}
	})
}

// FuzzDecode feeds arbitrary body bytes and content-type values through
// content.Decode. Oracle: it must never panic, regardless of the codec lookup
// outcome or base64 validity of the body.
func FuzzDecode(f *testing.F) {
	f.Add("application/json", `{"a":1}`)
	f.Add("application/octet-stream", "aGVsbG8=")     // valid base64
	f.Add("application/octet-stream", "not!base64!!") // invalid base64
	f.Add("application/protobuf", "")
	f.Add("text/plain", "plain text body")
	f.Add("application/unknown-type", "anything")
	f.Add("", "no content type -> raw passthrough")

	registry := DefaultRegistry()

	f.Fuzz(func(t *testing.T, contentType, body string) {
		msg := msgReader{
			body:    body,
			headers: map[string]string{},
		}
		// Route the content type either through a header or leave it unset.
		if contentType != "" {
			msg.headers["Content-Type"] = contentType
		}
		// Discard result: the oracle is "no panic". Errors are expected for
		// unknown types and malformed base64.
		_, _ = Decode(msg, registry)
	})
}
