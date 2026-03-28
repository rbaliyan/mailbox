// Package compress provides message body compression as a mailbox plugin.
//
// The CompressionPlugin implements mailbox.SendHook and compresses the message
// body during send. Decompression is handled client-side via the Decompress
// function or automatically by crypto.Open when used with encryption.
//
// Register this plugin before the encryption plugin for compress-then-encrypt:
//
//	svc, _ := mailbox.NewService(
//	    mailbox.WithStore(store),
//	    mailbox.WithPlugins(
//	        compress.NewPlugin(compress.Gzip),
//	        crypto.NewEncryptionPlugin(keys),
//	    ),
//	)
//
// Reading compressed messages:
//
//	msg, _ := mb.Get(ctx, msgID)
//	body, _ := compress.Open(msg)
package compress

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/rbaliyan/mailbox/store"
)

// ErrUnsupportedEncoding is returned for unknown compression encodings.
var ErrUnsupportedEncoding = errors.New("compress: unsupported content encoding")

// Compressor compresses and decompresses data.
type Compressor interface {
	// Algorithm returns the Content-Encoding value (e.g., "gzip", "zstd").
	Algorithm() string
	// Compress compresses data.
	Compress(data []byte) ([]byte, error)
	// Decompress decompresses data.
	Decompress(data []byte) ([]byte, error)
}

// Pre-built compressors.
var (
	// Gzip is a gzip compressor using default compression level.
	Gzip Compressor = &gzipCompressor{level: gzip.DefaultCompression}
	// GzipBest is a gzip compressor using best compression.
	GzipBest Compressor = &gzipCompressor{level: gzip.BestCompression}
)

// Plugin compresses message bodies before send.
// Implements mailbox.SendHook.
type Plugin struct {
	compressor Compressor
}

// NewPlugin creates a compression plugin with the given compressor.
func NewPlugin(c Compressor) *Plugin {
	return &Plugin{compressor: c}
}

func (p *Plugin) Name() string                 { return "compression" }
func (p *Plugin) Init(_ context.Context) error { return nil }
func (p *Plugin) Close(_ context.Context) error { return nil }
func (p *Plugin) AfterSend(_ context.Context, _ string, _ store.Message) error {
	return nil
}

// BeforeSend compresses the message body and sets the Content-Encoding header.
func (p *Plugin) BeforeSend(_ context.Context, _ string, draft store.DraftMessage) error {
	body := draft.GetBody()
	if len(body) == 0 {
		return nil
	}

	compressed, err := p.compressor.Compress([]byte(body))
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(compressed)
	draft.SetBody(encoded)
	draft.SetHeader(store.HeaderContentEncoding, p.compressor.Algorithm())
	return nil
}

// MessageReader is the minimal interface for reading compressed messages.
type MessageReader interface {
	GetBody() string
	GetHeaders() map[string]string
}

// IsCompressed returns true if the message has a Content-Encoding header.
func IsCompressed(msg MessageReader) bool {
	headers := msg.GetHeaders()
	return headers != nil && headers[store.HeaderContentEncoding] != ""
}

// Open decompresses a message body based on the Content-Encoding header.
// Returns the raw body if the message is not compressed.
func Open(msg MessageReader) ([]byte, error) {
	headers := msg.GetHeaders()
	encoding := ""
	if headers != nil {
		encoding = headers[store.HeaderContentEncoding]
	}
	if encoding == "" {
		return []byte(msg.GetBody()), nil
	}

	decoded, err := base64.StdEncoding.DecodeString(msg.GetBody())
	if err != nil {
		return nil, fmt.Errorf("compress: decode body: %w", err)
	}
	return Decompress(decoded, encoding)
}

// Decompress decompresses data using the specified encoding.
// Returns the data unchanged if encoding is empty.
func Decompress(data []byte, encoding string) ([]byte, error) {
	if encoding == "" {
		return data, nil
	}

	c, err := compressorForEncoding(encoding)
	if err != nil {
		return nil, err
	}
	return c.Decompress(data)
}

func compressorForEncoding(encoding string) (Compressor, error) {
	switch encoding {
	case "gzip":
		return Gzip, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedEncoding, encoding)
	}
}

// --- gzip compressor ---

type gzipCompressor struct {
	level int
}

func (c *gzipCompressor) Algorithm() string { return "gzip" }

func (c *gzipCompressor) Compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, c.level)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *gzipCompressor) Decompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
