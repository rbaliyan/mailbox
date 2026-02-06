package content

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// --- test mocks ---

// mockMessage implements store.MessageReader for Decode tests.
type mockMessage struct {
	body     string
	metadata map[string]any
}

func (m *mockMessage) GetID() string                    { return "" }
func (m *mockMessage) GetOwnerID() string                { return "" }
func (m *mockMessage) GetSenderID() string               { return "" }
func (m *mockMessage) GetSubject() string                { return "" }
func (m *mockMessage) GetBody() string                   { return m.body }
func (m *mockMessage) GetRecipientIDs() []string         { return nil }
func (m *mockMessage) GetMetadata() map[string]any       { return m.metadata }
func (m *mockMessage) GetAttachments() []store.Attachment { return nil }
func (m *mockMessage) GetCreatedAt() time.Time           { return time.Time{} }
func (m *mockMessage) GetUpdatedAt() time.Time           { return time.Time{} }

var _ store.MessageReader = (*mockMessage)(nil)

// --- Codec tests ---

func TestTextCodec_RoundTrip(t *testing.T) {
	codecs := []Codec{JSON, XML, Plain}
	input := []byte(`{"temperature":72,"unit":"F"}`)

	for _, c := range codecs {
		t.Run(c.ContentType(), func(t *testing.T) {
			encoded, err := c.Encode(input)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if encoded != string(input) {
				t.Errorf("Encode should pass through, got %q", encoded)
			}

			decoded, err := c.Decode(encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if string(decoded) != string(input) {
				t.Errorf("Decode mismatch: got %q, want %q", decoded, input)
			}
		})
	}
}

func TestBinaryCodec_RoundTrip(t *testing.T) {
	codecs := []Codec{Protobuf, MsgPack, OctetStream}
	input := []byte{0x08, 0x96, 0x01, 0x00, 0xFF, 0xFE} // arbitrary binary

	for _, c := range codecs {
		t.Run(c.ContentType(), func(t *testing.T) {
			encoded, err := c.Encode(input)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			expected := base64.StdEncoding.EncodeToString(input)
			if encoded != expected {
				t.Errorf("Encode: got %q, want %q", encoded, expected)
			}

			decoded, err := c.Decode(encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if len(decoded) != len(input) {
				t.Fatalf("Decode length: got %d, want %d", len(decoded), len(input))
			}
			for i := range input {
				if decoded[i] != input[i] {
					t.Errorf("Decode byte %d: got %x, want %x", i, decoded[i], input[i])
				}
			}
		})
	}
}

func TestBinaryCodec_DecodeInvalid(t *testing.T) {
	_, err := Protobuf.Decode("not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error decoding invalid base64")
	}
}

// --- Registry tests ---

func TestRegistry_LookupBuiltin(t *testing.T) {
	r := DefaultRegistry()

	tests := []struct {
		ct   string
		want bool
	}{
		{"application/json", true},
		{"application/xml", true},
		{"text/plain", true},
		{"application/protobuf", true},
		{"application/msgpack", true},
		{"application/octet-stream", true},
		{"application/unknown", false},
	}

	for _, tt := range tests {
		c, ok := r.Lookup(tt.ct)
		if ok != tt.want {
			t.Errorf("Lookup(%q): got ok=%v, want %v", tt.ct, ok, tt.want)
		}
		if ok && c.ContentType() != tt.ct {
			t.Errorf("Lookup(%q): codec content type = %q", tt.ct, c.ContentType())
		}
	}
}

func TestRegistry_RegisterOverrides(t *testing.T) {
	r := NewRegistry(JSON)

	custom := textCodec{ct: "application/json"}
	r.Register(custom)

	c, ok := r.Lookup("application/json")
	if !ok {
		t.Fatal("expected to find application/json")
	}
	if c.ContentType() != "application/json" {
		t.Errorf("content type = %q", c.ContentType())
	}
}

// --- Encode tests ---

func TestEncode_JSON(t *testing.T) {
	data, _ := json.Marshal(map[string]int{"temperature": 72})

	body, meta, err := Encode(JSON, data)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if body != string(data) {
		t.Errorf("body = %q, want %q", body, string(data))
	}
	if ct := meta[MetaContentType]; ct != "application/json" {
		t.Errorf("content_type = %v, want application/json", ct)
	}
	if _, ok := meta[MetaSchema]; ok {
		t.Error("schema should not be set when WithSchema is not used")
	}
}

func TestEncode_WithSchema(t *testing.T) {
	_, meta, err := Encode(JSON, []byte("{}"), WithSchema("sensor.reading/v1"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if s := meta[MetaSchema]; s != "sensor.reading/v1" {
		t.Errorf("schema = %v, want sensor.reading/v1", s)
	}
}

func TestEncode_Binary(t *testing.T) {
	input := []byte{0x08, 0x96, 0x01}

	body, meta, err := Encode(Protobuf, input)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	expected := base64.StdEncoding.EncodeToString(input)
	if body != expected {
		t.Errorf("body = %q, want %q", body, expected)
	}
	if ct := meta[MetaContentType]; ct != "application/protobuf" {
		t.Errorf("content_type = %v, want application/protobuf", ct)
	}
}

// --- Decode tests ---

func TestDecode_JSON(t *testing.T) {
	r := DefaultRegistry()
	msg := &mockMessage{
		body: `{"temperature":72}`,
		metadata: map[string]any{
			MetaContentType: "application/json",
		},
	}

	data, err := Decode(msg, r)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if string(data) != msg.body {
		t.Errorf("data = %q, want %q", data, msg.body)
	}
}

func TestDecode_Binary(t *testing.T) {
	r := DefaultRegistry()
	input := []byte{0x08, 0x96, 0x01}
	msg := &mockMessage{
		body: base64.StdEncoding.EncodeToString(input),
		metadata: map[string]any{
			MetaContentType: "application/protobuf",
		},
	}

	data, err := Decode(msg, r)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(data) != len(input) {
		t.Fatalf("length: got %d, want %d", len(data), len(input))
	}
	for i := range input {
		if data[i] != input[i] {
			t.Errorf("byte %d: got %x, want %x", i, data[i], input[i])
		}
	}
}

func TestDecode_PlainTextFallback(t *testing.T) {
	r := DefaultRegistry()
	msg := &mockMessage{
		body:     "just plain text",
		metadata: map[string]any{},
	}

	data, err := Decode(msg, r)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if string(data) != "just plain text" {
		t.Errorf("data = %q, want %q", data, "just plain text")
	}
}

func TestDecode_NilMetadata(t *testing.T) {
	r := DefaultRegistry()
	msg := &mockMessage{
		body:     "no metadata",
		metadata: nil,
	}

	data, err := Decode(msg, r)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if string(data) != "no metadata" {
		t.Errorf("data = %q", data)
	}
}

func TestDecode_UnsupportedContentType(t *testing.T) {
	r := NewRegistry() // empty registry
	msg := &mockMessage{
		body: "data",
		metadata: map[string]any{
			MetaContentType: "application/custom",
		},
	}

	_, err := Decode(msg, r)
	if err == nil {
		t.Fatal("expected error for unsupported content type")
	}
	if !isUnsupported(err) {
		t.Errorf("expected ErrUnsupportedContentType, got %v", err)
	}
}

func isUnsupported(err error) bool {
	for err != nil {
		if err == ErrUnsupportedContentType {
			return true
		}
		if u, ok := err.(interface{ Unwrap() error }); ok {
			err = u.Unwrap()
		} else {
			return false
		}
	}
	return false
}

// --- Metadata helper tests ---

func TestContentType(t *testing.T) {
	msg := &mockMessage{metadata: map[string]any{MetaContentType: "application/json"}}
	if ct := ContentType(msg); ct != "application/json" {
		t.Errorf("ContentType = %q", ct)
	}

	msg2 := &mockMessage{metadata: map[string]any{}}
	if ct := ContentType(msg2); ct != "" {
		t.Errorf("ContentType should be empty, got %q", ct)
	}
}

func TestSchema(t *testing.T) {
	msg := &mockMessage{metadata: map[string]any{MetaSchema: "sensor.reading/v1"}}
	if s := Schema(msg); s != "sensor.reading/v1" {
		t.Errorf("Schema = %q", s)
	}

	msg2 := &mockMessage{metadata: map[string]any{}}
	if s := Schema(msg2); s != "" {
		t.Errorf("Schema should be empty, got %q", s)
	}
}

// --- Round-trip: Encode then Decode ---

func TestRoundTrip_JSON(t *testing.T) {
	type Reading struct {
		Temperature int    `json:"temperature"`
		Unit        string `json:"unit"`
	}

	original := Reading{Temperature: 72, Unit: "F"}
	data, _ := json.Marshal(original)

	// Encode
	body, meta, err := Encode(JSON, data, WithSchema("sensor.reading/v1"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Simulate storage: body + metadata → stored message
	msg := &mockMessage{body: body, metadata: map[string]any(meta)}

	// Decode from message
	r := DefaultRegistry()
	raw, err := Decode(msg, r)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	var got Reading
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != original {
		t.Errorf("round-trip: got %+v, want %+v", got, original)
	}
	if s := Schema(msg); s != "sensor.reading/v1" {
		t.Errorf("schema = %q", s)
	}
}

func TestRoundTrip_Protobuf(t *testing.T) {
	// Simulate protobuf-marshalled bytes
	original := []byte{0x08, 0x96, 0x01, 0x12, 0x07, 0x74, 0x65, 0x73, 0x74, 0x69, 0x6e, 0x67}

	body, meta, err := Encode(Protobuf, original)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	msg := &mockMessage{body: body, metadata: map[string]any(meta)}

	r := DefaultRegistry()
	raw, err := Decode(msg, r)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if len(raw) != len(original) {
		t.Fatalf("length: got %d, want %d", len(raw), len(original))
	}
	for i := range original {
		if raw[i] != original[i] {
			t.Errorf("byte %d: got %x, want %x", i, raw[i], original[i])
		}
	}
}
