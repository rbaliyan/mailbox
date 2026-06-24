package compress_test

import (
	"bytes"
	"testing"

	"github.com/rbaliyan/mailbox/compress"
)

// FuzzGzipRoundTrip asserts that gzip compression round-trips: decompressing a
// freshly compressed payload yields the original bytes for any input.
func FuzzGzipRoundTrip(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("hello world"))
	f.Add([]byte("the quick brown fox jumps over the lazy dog"))
	f.Add(bytes.Repeat([]byte("a"), 4096))

	f.Fuzz(func(t *testing.T, data []byte) {
		compressed, err := compress.Gzip.Compress(data)
		if err != nil {
			t.Fatalf("Compress: %v", err)
		}
		got, err := compress.Decompress(compressed, "gzip")
		if err != nil {
			t.Fatalf("Decompress: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(data))
		}
	})
}

// FuzzDecompress feeds arbitrary bytes to the gzip decompressor. Malformed input
// must return an error, never panic and never return without a definite result.
func FuzzDecompress(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x1f, 0x8b}) // gzip magic with no body
	f.Add([]byte("not gzip at all"))
	// A valid gzip stream so the success path is exercised too.
	if seed, err := compress.Gzip.Compress([]byte("seed payload")); err == nil {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic; an error is the expected outcome for malformed input.
		_, _ = compress.Decompress(data, "gzip")
	})
}
