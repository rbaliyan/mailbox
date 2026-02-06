package content

import "encoding/base64"

// Built-in codecs for common content types.
//
// Text-safe codecs (JSON, XML, Plain) are zero-cost pass-throughs since
// these formats are already valid UTF-8 text. Binary codecs (Protobuf,
// MsgPack) use standard base64 encoding.
var (
	// JSON is a pass-through codec for application/json content.
	// JSON is already text-safe UTF-8, so no encoding is needed.
	JSON Codec = textCodec{ct: "application/json"}

	// XML is a pass-through codec for application/xml content.
	XML Codec = textCodec{ct: "application/xml"}

	// Plain is a pass-through codec for text/plain content.
	// This is useful when you want to explicitly mark a message as plain text
	// rather than leaving the content_type unset.
	Plain Codec = textCodec{ct: "text/plain"}

	// Protobuf is a base64 codec for application/protobuf content.
	// Binary protobuf bytes are base64-encoded for text-safe storage.
	Protobuf Codec = binaryCodec{ct: "application/protobuf"}

	// MsgPack is a base64 codec for application/msgpack content.
	// Binary msgpack bytes are base64-encoded for text-safe storage.
	MsgPack Codec = binaryCodec{ct: "application/msgpack"}

	// OctetStream is a base64 codec for application/octet-stream content.
	// Arbitrary binary data is base64-encoded for text-safe storage.
	OctetStream Codec = binaryCodec{ct: "application/octet-stream"}
)

// DefaultRegistry returns a registry pre-loaded with all built-in codecs.
func DefaultRegistry() *Registry {
	return NewRegistry(JSON, XML, Plain, Protobuf, MsgPack, OctetStream)
}

// textCodec is a zero-cost pass-through for text-safe content types.
// The bytes are cast to/from string without any transformation.
type textCodec struct {
	ct string
}

func (c textCodec) ContentType() string                { return c.ct }
func (c textCodec) Encode(data []byte) (string, error) { return string(data), nil }
func (c textCodec) Decode(body string) ([]byte, error) { return []byte(body), nil }

// binaryCodec uses standard base64 encoding for binary content types.
type binaryCodec struct {
	ct string
}

func (c binaryCodec) ContentType() string { return c.ct }

func (c binaryCodec) Encode(data []byte) (string, error) {
	return base64.StdEncoding.EncodeToString(data), nil
}

func (c binaryCodec) Decode(body string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(body)
}
