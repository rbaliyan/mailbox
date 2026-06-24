package content

import (
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// benchSink defends the benchmarked codec results from dead-code elimination.
// The compiler cannot prove the assigned value is unused, so the Encode/Decode
// work cannot be elided.
var benchSink any

// bodySizes are the payload sizes exercised by the content codec benchmarks.
var bodySizes = []struct {
	name string
	size int
}{
	{"256B", 256},
	{"4KB", 4 * 1024},
	{"64KB", 64 * 1024},
}

// benchPayload returns a deterministic random byte slice of the given size.
func benchPayload(b *testing.B, size int) []byte {
	b.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		b.Fatalf("payload: %v", err)
	}
	return buf
}

// benchCodecs are the codecs registered in DefaultRegistry that are worth
// measuring: the JSON pass-through (text-safe, zero-copy) and the base64
// binary codec. They have very different cost profiles.
var benchCodecs = []struct {
	name  string
	codec Codec
}{
	{"json", JSON},
	{"base64", OctetStream},
}

// BenchmarkEncodeWithHeaders measures EncodeWithHeaders across the registered
// codecs and body sizes. This exercises the real codec.Encode path plus the
// header map construction returned to the caller.
func BenchmarkEncodeWithHeaders(b *testing.B) {
	for _, c := range benchCodecs {
		for _, bs := range bodySizes {
			b.Run(fmt.Sprintf("%s/%s", c.name, bs.name), func(b *testing.B) {
				data := benchPayload(b, bs.size)
				b.ReportAllocs()
				for b.Loop() {
					body, headers, err := EncodeWithHeaders(c.codec, data, WithSchema("bench/v1"))
					if err != nil {
						b.Fatal(err)
					}
					benchSink = body
					benchSink = headers
				}
			})
		}
	}
}

// BenchmarkDecode measures Decode across the registered codecs and body sizes.
// Decode looks up the codec by Content-Type header in the registry and runs
// the real codec.Decode path. A message is pre-encoded outside b.Loop().
func BenchmarkDecode(b *testing.B) {
	registry := DefaultRegistry()
	for _, c := range benchCodecs {
		for _, bs := range bodySizes {
			b.Run(fmt.Sprintf("%s/%s", c.name, bs.name), func(b *testing.B) {
				data := benchPayload(b, bs.size)
				body, headers, err := EncodeWithHeaders(c.codec, data)
				if err != nil {
					b.Fatalf("seed encode: %v", err)
				}
				msg := benchMessage{body: body, headers: headers}
				b.ReportAllocs()
				for b.Loop() {
					raw, err := Decode(msg, registry)
					if err != nil {
						b.Fatal(err)
					}
					benchSink = raw
				}
			})
		}
	}
}

// benchMessage is a store.MessageReader carrying a body and headers, the only
// fields Decode reads. The remaining accessors return zero values to satisfy
// the full interface.
type benchMessage struct {
	body    string
	headers map[string]string
}

func (m benchMessage) GetBody() string                  { return m.body }
func (m benchMessage) GetHeaders() map[string]string    { return m.headers }
func (m benchMessage) GetMetadata() map[string]any      { return nil }
func (benchMessage) GetID() string                      { return "" }
func (benchMessage) GetOwnerID() string                 { return "" }
func (benchMessage) GetSenderID() string                { return "" }
func (benchMessage) GetSubject() string                 { return "" }
func (benchMessage) GetRecipientIDs() []string          { return nil }
func (benchMessage) GetAttachments() []store.Attachment { return nil }
func (benchMessage) GetCreatedAt() time.Time            { return time.Time{} }
func (benchMessage) GetUpdatedAt() time.Time            { return time.Time{} }
func (benchMessage) GetExpiresAt() *time.Time           { return nil }
func (benchMessage) GetAvailableAt() *time.Time         { return nil }
func (benchMessage) GetThreadID() string                { return "" }
func (benchMessage) GetReplyToID() string               { return "" }
func (benchMessage) GetExternalID() string              { return "" }

// Compile-time check that benchMessage satisfies the reader Decode needs.
var _ store.MessageReader = benchMessage{}
