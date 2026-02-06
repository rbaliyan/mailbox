package mailbox

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rbaliyan/mailbox/store"
)

// MessageLimits holds all message validation limits.
// Used to pass limits to validation functions.
type MessageLimits struct {
	MaxSubjectLength   int
	MaxBodySize        int
	MaxAttachmentSize  int64
	MaxAttachmentCount int
	MaxRecipientCount  int
	MaxMetadataSize    int
	MaxMetadataKeys    int
}

// Validation constants for message content.
// These are the default values; use MessageLimits for configurable validation.
const (
	// Metadata limits
	MaxMetadataKeyLength = 256 // Maximum length of a metadata key

	// MaxTagIDLength is the maximum length for a tag ID.
	MaxTagIDLength = 256

	// MinSubjectLength is the minimum subject length (non-empty after trimming)
	MinSubjectLength = 1
)

// DefaultLimits returns the default message limits.
func DefaultLimits() MessageLimits {
	return MessageLimits{
		MaxSubjectLength:   DefaultMaxSubjectLength,
		MaxBodySize:        DefaultMaxBodySize,
		MaxAttachmentSize:  DefaultMaxAttachmentSize,
		MaxAttachmentCount: DefaultMaxAttachmentCount,
		MaxRecipientCount:  DefaultMaxRecipientCount,
		MaxMetadataSize:    DefaultMaxMetadataSize,
		MaxMetadataKeys:    DefaultMaxMetadataKeys,
	}
}

// ValidateMetadata validates metadata against size and key constraints using default limits.
// For configurable limits, use ValidateMetadataWithLimits.
func ValidateMetadata(metadata map[string]any) error {
	return ValidateMetadataWithLimits(metadata, DefaultLimits())
}

// ValidateMetadataWithLimits validates metadata against configurable limits.
func ValidateMetadataWithLimits(metadata map[string]any, limits MessageLimits) error {
	if metadata == nil {
		return nil
	}

	if len(metadata) > limits.MaxMetadataKeys {
		return fmt.Errorf("%w: too many keys (%d > %d)", ErrInvalidMetadata, len(metadata), limits.MaxMetadataKeys)
	}

	for key := range metadata {
		if len(key) > MaxMetadataKeyLength {
			// Truncate key for error message, handling short keys
			truncated := key
			if len(key) > 50 {
				truncated = key[:50]
			}
			return fmt.Errorf("%w: key '%s...' exceeds max length %d", ErrMetadataKeyTooLong, truncated, MaxMetadataKeyLength)
		}
		if key == "" {
			return fmt.Errorf("%w: empty key not allowed", ErrInvalidMetadata)
		}
	}

	// Check total size by serializing to JSON
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("%w: cannot serialize metadata: %v", ErrInvalidMetadata, err)
	}

	if len(data) > limits.MaxMetadataSize {
		return fmt.Errorf("%w: metadata size %d exceeds max %d bytes", ErrMetadataTooLarge, len(data), limits.MaxMetadataSize)
	}

	return nil
}

// ValidateMetadataKey validates a single metadata key.
func ValidateMetadataKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty key not allowed", ErrInvalidMetadata)
	}
	if len(key) > MaxMetadataKeyLength {
		return fmt.Errorf("%w: key exceeds max length %d", ErrMetadataKeyTooLong, MaxMetadataKeyLength)
	}
	return nil
}

// ValidateSubject validates a message subject using default limits.
// For configurable limits, use ValidateSubjectWithLimits.
func ValidateSubject(subject string) error {
	return ValidateSubjectWithLimits(subject, DefaultLimits())
}

// ValidateSubjectWithLimits validates a message subject against configurable limits.
func ValidateSubjectWithLimits(subject string, limits MessageLimits) error {
	// Trim whitespace for validation
	trimmed := strings.TrimSpace(subject)
	if len(trimmed) < MinSubjectLength {
		return ErrEmptySubject
	}

	if len(subject) > limits.MaxSubjectLength {
		return fmt.Errorf("%w: subject length %d exceeds max %d", ErrSubjectTooLong, len(subject), limits.MaxSubjectLength)
	}

	// Check for valid UTF-8 and no control characters (except newline/tab)
	if !utf8.ValidString(subject) {
		return fmt.Errorf("%w: subject contains invalid UTF-8", ErrInvalidContent)
	}

	for _, r := range subject {
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return fmt.Errorf("%w: subject contains control character U+%04X", ErrInvalidContent, r)
		}
	}

	return nil
}

// ValidateBody validates a message body using default limits.
// For configurable limits, use ValidateBodyWithLimits.
func ValidateBody(body string) error {
	return ValidateBodyWithLimits(body, DefaultLimits())
}

// ValidateBodyWithLimits validates a message body against configurable limits.
func ValidateBodyWithLimits(body string, limits MessageLimits) error {
	if len(body) > limits.MaxBodySize {
		return fmt.Errorf("%w: body size %d exceeds max %d bytes", ErrBodyTooLarge, len(body), limits.MaxBodySize)
	}

	// Check for valid UTF-8
	if !utf8.ValidString(body) {
		return fmt.Errorf("%w: body contains invalid UTF-8", ErrInvalidContent)
	}

	// Check for null bytes which could indicate injection attempts
	if strings.ContainsRune(body, '\x00') {
		return fmt.Errorf("%w: body contains null bytes", ErrInvalidContent)
	}

	return nil
}

// ValidateMessageContent validates subject and body together using default limits.
func ValidateMessageContent(subject, body string) error {
	return ValidateMessageContentWithLimits(subject, body, DefaultLimits())
}

// ValidateMessageContentWithLimits validates subject and body with configurable limits.
func ValidateMessageContentWithLimits(subject, body string, limits MessageLimits) error {
	if err := ValidateSubjectWithLimits(subject, limits); err != nil {
		return err
	}
	return ValidateBodyWithLimits(body, limits)
}

// ValidateRecipients validates the recipient list.
func ValidateRecipients(recipientIDs []string, limits MessageLimits) error {
	if len(recipientIDs) == 0 {
		return ErrEmptyRecipients
	}

	if len(recipientIDs) > limits.MaxRecipientCount {
		return fmt.Errorf("%w: recipient count %d exceeds max %d", ErrTooManyRecipients, len(recipientIDs), limits.MaxRecipientCount)
	}

	// Check for empty recipient IDs (duplicates are silently deduplicated at send time)
	for _, id := range recipientIDs {
		if id == "" {
			return fmt.Errorf("%w: empty recipient ID", ErrInvalidRecipient)
		}
	}

	return nil
}

// ValidateAttachments validates the attachment list.
func ValidateAttachments(attachments []store.Attachment, limits MessageLimits) error {
	return ValidateAttachmentsWithMIME(attachments, limits, nil, nil)
}

// ValidateAttachmentsWithMIME validates attachments with MIME type restrictions.
// allowedTypes: if non-empty, only these MIME types are allowed.
// blockedTypes: these MIME types are always blocked, even if in allowedTypes.
func ValidateAttachmentsWithMIME(attachments []store.Attachment, limits MessageLimits, allowedTypes, blockedTypes []string) error {
	if len(attachments) > limits.MaxAttachmentCount {
		return fmt.Errorf("%w: attachment count %d exceeds max %d", ErrTooManyAttachments, len(attachments), limits.MaxAttachmentCount)
	}

	for _, a := range attachments {
		if a.GetSize() > limits.MaxAttachmentSize {
			return fmt.Errorf("%w: attachment %q size %d exceeds max %d bytes",
				ErrAttachmentTooLarge, a.GetFilename(), a.GetSize(), limits.MaxAttachmentSize)
		}
		if a.GetFilename() == "" {
			return fmt.Errorf("%w: attachment filename is required", ErrInvalidAttachment)
		}
		// Validate MIME type
		contentType := a.GetContentType()
		if err := ValidateMIMEType(contentType, allowedTypes, blockedTypes); err != nil {
			return fmt.Errorf("%w: attachment %q: %v", ErrInvalidMIMEType, a.GetFilename(), err)
		}
	}

	return nil
}

// ValidateMIMEType validates a MIME type against allowed and blocked lists.
// Returns nil if the MIME type is valid.
func ValidateMIMEType(contentType string, allowedTypes, blockedTypes []string) error {
	// Normalize content type (remove parameters like charset)
	normalized := normalizeMIMEType(contentType)

	if normalized == "" {
		return fmt.Errorf("empty content type")
	}

	// Check if blocked
	for _, blocked := range blockedTypes {
		if matchMIMEType(normalized, blocked) {
			return fmt.Errorf("content type %q is blocked", contentType)
		}
	}

	// If allowedTypes is specified, check if allowed
	if len(allowedTypes) > 0 {
		allowed := false
		for _, a := range allowedTypes {
			if matchMIMEType(normalized, a) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("content type %q is not allowed", contentType)
		}
	}

	return nil
}

// normalizeMIMEType extracts the base MIME type without parameters.
// e.g., "text/plain; charset=utf-8" -> "text/plain"
func normalizeMIMEType(contentType string) string {
	ct := strings.TrimSpace(contentType)
	if ct == "" {
		return ""
	}
	// Split on semicolon to remove parameters
	parts := strings.SplitN(ct, ";", 2)
	return strings.ToLower(strings.TrimSpace(parts[0]))
}

// matchMIMEType checks if contentType matches the pattern.
// Supports wildcards: "image/*" matches "image/png", "image/jpeg", etc.
func matchMIMEType(contentType, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	contentType = strings.ToLower(strings.TrimSpace(contentType))

	if pattern == contentType {
		return true
	}

	// Check for wildcard patterns like "image/*"
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		return strings.HasPrefix(contentType, prefix+"/")
	}

	return false
}

// DefaultBlockedMIMETypes returns MIME types that are commonly blocked for security.
func DefaultBlockedMIMETypes() []string {
	return []string{
		"application/x-msdownload",      // Windows executable
		"application/x-executable",      // Generic executable
		"application/x-msdos-program",   // DOS executable
		"application/x-sh",              // Shell script
		"application/x-shellscript",     // Shell script
		"application/x-bat",             // Batch file
		"application/x-msi",             // Windows installer
		"application/vnd.microsoft.portable-executable", // PE executable
		"application/x-dosexec",         // DOS executable
	}
}

// SafeAttachmentMIMETypes returns commonly allowed safe MIME types.
func SafeAttachmentMIMETypes() []string {
	return []string{
		// Documents
		"application/pdf",
		"application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.ms-excel",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-powerpoint",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"text/plain",
		"text/csv",
		"text/html",
		// Images
		"image/*",
		// Audio
		"audio/*",
		// Video
		"video/*",
		// Archives (be careful with these)
		"application/zip",
		"application/gzip",
		"application/x-tar",
	}
}

// validateMessageReader validates recipients, content, metadata, and attachments
// for any type that implements store.MessageReader.
func validateMessageReader(r store.MessageReader, limits MessageLimits) error {
	if err := ValidateRecipients(r.GetRecipientIDs(), limits); err != nil {
		return err
	}
	if err := ValidateMessageContentWithLimits(r.GetSubject(), r.GetBody(), limits); err != nil {
		return err
	}
	if err := ValidateMetadataWithLimits(r.GetMetadata(), limits); err != nil {
		return err
	}
	return ValidateAttachments(r.GetAttachments(), limits)
}

// ValidateMessage performs full validation of a message.
func ValidateMessage(msg store.Message, limits MessageLimits) error {
	return validateMessageReader(msg, limits)
}

// ValidateDraft performs full validation of a draft before sending.
func ValidateDraft(draft store.DraftMessage, limits MessageLimits) error {
	return validateMessageReader(draft, limits)
}
