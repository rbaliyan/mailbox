// Package content provides a content-type codec layer for mailbox messages.
//
// Mailbox stores message bodies as plain text strings. This package provides
// a convention for encoding structured or binary content into text-safe bodies
// and decoding them back, using metadata to carry content type and schema
// information.
//
// The content package does NOT modify any mailbox interfaces. It operates
// entirely through the existing public API (SetBody, SetMetadata, GetBody,
// GetMetadata). Mailbox remains text-first; this package is an opt-in layer
// on top.
//
// # Metadata Convention
//
// Structured messages use these reserved metadata keys:
//
//   - content_type: MIME type (e.g., "application/json", "application/protobuf")
//   - schema: optional schema identifier (e.g., "sensor.reading/v1")
//
// Messages without content_type metadata are plain text. No codec is needed
// to read or write them.
//
// # Codec Interface
//
// A [Codec] converts between raw bytes and a text-safe string:
//
//   - Text-safe formats (JSON, XML) pass through unchanged.
//   - Binary formats (protobuf, msgpack) are base64-encoded for storage.
//
// The application handles serialization (struct to bytes) separately.
// The codec handles only the text-encoding concern.
//
// # Usage
//
// Sending a structured message:
//
//	data, _ := json.Marshal(sensorReading)
//	body, meta, _ := content.Encode(content.JSON, data, content.WithSchema("sensor.reading/v1"))
//	draft, _ := mb.Compose()
//	draft.SetSubject("Reading").SetRecipients("consumer-svc").SetBody(body)
//	for k, v := range meta {
//	    draft.SetMetadata(k, v)
//	}
//	draft.Send(ctx)
//
// Reading a structured message:
//
//	msg, _ := mb.Get(ctx, id)
//	raw, _ := content.Decode(msg, registry)
//	var reading SensorReading
//	json.Unmarshal(raw, &reading)
//
// # Plugin Integration
//
// Mailbox [SendHook] plugins can inspect the metadata convention to validate,
// route, or forward structured messages to external systems. The content
// package provides [ContentType] and [Schema] helpers for reading metadata
// without hardcoding key names.
package content

import (
	"errors"
	"fmt"
	"sync"

	"github.com/rbaliyan/mailbox/store"
)

// Metadata is a set of key-value pairs to apply to a draft or send request.
type Metadata map[string]any

// Metadata keys used by the content codec convention.
// These are set on message metadata by [Encode] and read by [Decode].
// Plugins and external systems should use these constants instead of
// hardcoding string values.
const (
	// MetaContentType is the metadata key for the MIME content type.
	// Example values: "application/json", "application/protobuf", "text/plain".
	MetaContentType = "content_type"

	// MetaSchema is the optional metadata key for a schema identifier.
	// The format is application-defined. Examples: "sensor.reading/v1", "order.placed/v2".
	MetaSchema = "schema"
)

// Sentinel errors.
var (
	// ErrUnsupportedContentType is returned when no codec is registered for a content type.
	ErrUnsupportedContentType = errors.New("content: unsupported content type")

	// ErrEncoding is returned when a codec fails to encode data.
	ErrEncoding = errors.New("content: encoding failed")

	// ErrDecoding is returned when a codec fails to decode a body.
	ErrDecoding = errors.New("content: decoding failed")
)

// Codec converts between raw bytes and a text-safe string representation.
//
// Implementations handle a specific content type. Text-safe formats (JSON, XML)
// typically pass through unchanged. Binary formats (protobuf, msgpack) use
// base64 encoding.
//
// The application is responsible for serializing structs to bytes before
// encoding and deserializing bytes after decoding. The codec handles only
// the text-safety concern.
type Codec interface {
	// ContentType returns the MIME type this codec handles.
	ContentType() string

	// Encode converts raw bytes to a text-safe string for storage in the
	// message body.
	Encode(data []byte) (string, error)

	// Decode converts a text body back to the original raw bytes.
	Decode(body string) ([]byte, error)
}

// Registry maps content types to codecs.
// It is safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	codecs map[string]Codec
}

// NewRegistry creates a registry pre-loaded with the given codecs.
func NewRegistry(codecs ...Codec) *Registry {
	r := &Registry{
		codecs: make(map[string]Codec, len(codecs)),
	}
	for _, c := range codecs {
		r.codecs[c.ContentType()] = c
	}
	return r
}

// Register adds a codec to the registry. If a codec for the same content type
// already exists, it is replaced.
func (r *Registry) Register(c Codec) {
	r.mu.Lock()
	r.codecs[c.ContentType()] = c
	r.mu.Unlock()
}

// Lookup returns the codec for the given content type.
// Returns false if no codec is registered for the content type.
func (r *Registry) Lookup(contentType string) (Codec, bool) {
	r.mu.RLock()
	c, ok := r.codecs[contentType]
	r.mu.RUnlock()
	return c, ok
}

// Encode encodes data using the codec and returns the text-safe body string
// along with metadata entries (content_type, and optionally schema) that
// should be set on the draft or send request.
//
// The returned [Metadata] is intentionally separate from the body so that
// Encode works with any draft type (mailbox.DraftComposer, store.DraftMessage,
// or mailbox.SendRequest) without depending on their differing method signatures.
//
// Options:
//   - [WithSchema] sets the schema metadata key.
func Encode(codec Codec, data []byte, opts ...EncodeOption) (string, Metadata, error) {
	body, err := codec.Encode(data)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrEncoding, err)
	}

	meta := Metadata{
		MetaContentType: codec.ContentType(),
	}

	var o encodeOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.schema != "" {
		meta[MetaSchema] = o.schema
	}

	return body, meta, nil
}

// Decode reads the content_type from message metadata, looks up the
// corresponding codec in the registry, and decodes the body to raw bytes.
//
// If no content_type metadata is set, the body is returned as raw bytes
// (plain text fallback).
func Decode(msg store.MessageReader, registry *Registry) ([]byte, error) {
	ct := ContentType(msg)
	if ct == "" {
		return []byte(msg.GetBody()), nil
	}

	codec, ok := registry.Lookup(ct)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedContentType, ct)
	}

	data, err := codec.Decode(msg.GetBody())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecoding, err)
	}
	return data, nil
}

// ContentType returns the content type from message metadata.
// Returns an empty string if no content_type metadata is set.
func ContentType(msg store.MessageReader) string {
	ct, _ := msg.GetMetadata()[MetaContentType].(string)
	return ct
}

// Schema returns the schema identifier from message metadata.
// Returns an empty string if no schema metadata is set.
func Schema(msg store.MessageReader) string {
	s, _ := msg.GetMetadata()[MetaSchema].(string)
	return s
}

// EncodeOption configures Encode behavior.
type EncodeOption func(*encodeOptions)

type encodeOptions struct {
	schema string
}

// WithSchema sets the schema metadata key on the message.
func WithSchema(schema string) EncodeOption {
	return func(o *encodeOptions) {
		o.schema = schema
	}
}
