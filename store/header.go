package store

// Well-known header keys for message headers.
// Headers carry protocol-level metadata (like HTTP headers), while Metadata
// carries application-level arbitrary data. Headers are always string→string.
const (
	// HeaderContentType is the MIME type of the message body.
	// Example: "application/json", "text/plain".
	HeaderContentType = "Content-Type"

	// HeaderContentLength is the byte length of the message body.
	// Auto-populated at send time if not already set.
	HeaderContentLength = "Content-Length"

	// HeaderContentEncoding is the encoding applied to the body.
	// Example: "gzip", "base64".
	HeaderContentEncoding = "Content-Encoding"

	// HeaderSchema is an application-defined schema identifier.
	// Example: "sensor.reading/v1", "order.placed/v2".
	HeaderSchema = "Schema"

	// HeaderPriority is the message priority level.
	// Example: "high", "normal", "low".
	HeaderPriority = "Priority"

	// HeaderCorrelationID links related messages for tracing.
	HeaderCorrelationID = "Correlation-ID"

	// HeaderExpires is the expiration timestamp (RFC 3339).
	HeaderExpires = "Expires"

	// HeaderReplyToAddress is the address to send replies to,
	// when it differs from the sender.
	HeaderReplyToAddress = "Reply-To-Address"

	// HeaderCustomID is a caller-defined external identifier.
	HeaderCustomID = "Custom-ID"
)
