package mailbox

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

func TestValidateSubject(t *testing.T) {
	tests := []struct {
		name      string
		subject   string
		wantErr   error
		errString string
	}{
		{
			name:    "valid subject",
			subject: "Hello World",
			wantErr: nil,
		},
		{
			name:    "valid subject with newline",
			subject: "Hello\nWorld",
			wantErr: nil,
		},
		{
			name:    "valid subject with tab",
			subject: "Hello\tWorld",
			wantErr: nil,
		},
		{
			name:    "empty subject",
			subject: "",
			wantErr: ErrEmptySubject,
		},
		{
			name:    "whitespace only subject",
			subject: "   \t\n  ",
			wantErr: ErrEmptySubject,
		},
		{
			name:      "subject with control character",
			subject:   "Hello\x00World",
			wantErr:   ErrInvalidContent,
			errString: "control character",
		},
		{
			name:    "subject at max length",
			subject: strings.Repeat("a", DefaultMaxSubjectLength),
			wantErr: nil,
		},
		{
			name:    "subject exceeds max length",
			subject: strings.Repeat("a", DefaultMaxSubjectLength+1),
			wantErr: ErrSubjectTooLong,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSubject(tt.subject)
			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", tt.wantErr)
					return
				}
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("expected error %v, got %v", tt.wantErr, err)
				}
				if tt.errString != "" && !strings.Contains(err.Error(), tt.errString) {
					t.Errorf("expected error to contain %q, got %q", tt.errString, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateSubjectWithLimits(t *testing.T) {
	limits := MessageLimits{
		MaxSubjectLength: 10,
	}

	t.Run("subject within custom limit", func(t *testing.T) {
		err := ValidateSubjectWithLimits("Hello", limits)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("subject exceeds custom limit", func(t *testing.T) {
		err := ValidateSubjectWithLimits("Hello World!", limits)
		if !errors.Is(err, ErrSubjectTooLong) {
			t.Errorf("expected ErrSubjectTooLong, got %v", err)
		}
	})
}

func TestValidateBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErr   error
		errString string
	}{
		{
			name:    "valid body",
			body:    "This is a valid message body.",
			wantErr: nil,
		},
		{
			name:    "empty body is valid",
			body:    "",
			wantErr: nil,
		},
		{
			name:    "body with unicode",
			body:    "Hello 世界! 🎉",
			wantErr: nil,
		},
		{
			name:      "body with null bytes",
			body:      "Hello\x00World",
			wantErr:   ErrInvalidContent,
			errString: "null bytes",
		},
		{
			name:    "body at max size",
			body:    strings.Repeat("a", DefaultMaxBodySize),
			wantErr: nil,
		},
		{
			name:    "body exceeds max size",
			body:    strings.Repeat("a", DefaultMaxBodySize+1),
			wantErr: ErrBodyTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBody(tt.body)
			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", tt.wantErr)
					return
				}
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("expected error %v, got %v", tt.wantErr, err)
				}
				if tt.errString != "" && !strings.Contains(err.Error(), tt.errString) {
					t.Errorf("expected error to contain %q, got %q", tt.errString, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateBodyWithLimits(t *testing.T) {
	limits := MessageLimits{
		MaxBodySize: 100,
	}

	t.Run("body within custom limit", func(t *testing.T) {
		err := ValidateBodyWithLimits("Short body", limits)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("body exceeds custom limit", func(t *testing.T) {
		err := ValidateBodyWithLimits(strings.Repeat("x", 101), limits)
		if !errors.Is(err, ErrBodyTooLarge) {
			t.Errorf("expected ErrBodyTooLarge, got %v", err)
		}
	})
}

func TestValidateMetadata(t *testing.T) {
	t.Run("nil metadata is valid", func(t *testing.T) {
		err := ValidateMetadata(nil)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty metadata is valid", func(t *testing.T) {
		err := ValidateMetadata(map[string]any{})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("valid metadata", func(t *testing.T) {
		err := ValidateMetadata(map[string]any{
			"key1": "value1",
			"key2": 42,
			"key3": true,
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty key not allowed", func(t *testing.T) {
		err := ValidateMetadata(map[string]any{
			"":    "value",
			"key": "value",
		})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("expected ErrInvalidMetadata, got %v", err)
		}
	})

	t.Run("key too long", func(t *testing.T) {
		longKey := strings.Repeat("k", MaxMetadataKeyLength+1)
		err := ValidateMetadata(map[string]any{
			longKey: "value",
		})
		if !errors.Is(err, ErrMetadataKeyTooLong) {
			t.Errorf("expected ErrMetadataKeyTooLong, got %v", err)
		}
	})

	t.Run("too many keys", func(t *testing.T) {
		limits := MessageLimits{
			MaxMetadataKeys: 2,
			MaxMetadataSize: 64 * 1024,
		}
		metadata := map[string]any{
			"key1": "value1",
			"key2": "value2",
			"key3": "value3",
		}
		err := ValidateMetadataWithLimits(metadata, limits)
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("expected ErrInvalidMetadata, got %v", err)
		}
	})

	t.Run("metadata too large", func(t *testing.T) {
		limits := MessageLimits{
			MaxMetadataKeys: 100,
			MaxMetadataSize: 10, // Very small
		}
		metadata := map[string]any{
			"key": "value that exceeds the limit",
		}
		err := ValidateMetadataWithLimits(metadata, limits)
		if !errors.Is(err, ErrMetadataTooLarge) {
			t.Errorf("expected ErrMetadataTooLarge, got %v", err)
		}
	})
}

func TestValidateMetadataKey(t *testing.T) {
	t.Run("valid key", func(t *testing.T) {
		err := ValidateMetadataKey("valid_key")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty key", func(t *testing.T) {
		err := ValidateMetadataKey("")
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("expected ErrInvalidMetadata, got %v", err)
		}
	})

	t.Run("key too long", func(t *testing.T) {
		err := ValidateMetadataKey(strings.Repeat("k", MaxMetadataKeyLength+1))
		if !errors.Is(err, ErrMetadataKeyTooLong) {
			t.Errorf("expected ErrMetadataKeyTooLong, got %v", err)
		}
	})
}

func TestValidateRecipients(t *testing.T) {
	limits := DefaultLimits()

	t.Run("valid recipients", func(t *testing.T) {
		err := ValidateRecipients([]string{"user1", "user2", "user3"}, limits)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty recipients", func(t *testing.T) {
		err := ValidateRecipients([]string{}, limits)
		if !errors.Is(err, ErrEmptyRecipients) {
			t.Errorf("expected ErrEmptyRecipients, got %v", err)
		}
	})

	t.Run("nil recipients", func(t *testing.T) {
		err := ValidateRecipients(nil, limits)
		if !errors.Is(err, ErrEmptyRecipients) {
			t.Errorf("expected ErrEmptyRecipients, got %v", err)
		}
	})

	t.Run("empty recipient ID", func(t *testing.T) {
		err := ValidateRecipients([]string{"user1", "", "user3"}, limits)
		if !errors.Is(err, ErrInvalidRecipient) {
			t.Errorf("expected ErrInvalidRecipient, got %v", err)
		}
	})

	t.Run("duplicate recipient", func(t *testing.T) {
		err := ValidateRecipients([]string{"user1", "user2", "user1"}, limits)
		if !errors.Is(err, ErrInvalidRecipient) {
			t.Errorf("expected ErrInvalidRecipient, got %v", err)
		}
		if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("expected error to mention 'duplicate', got %v", err)
		}
	})

	t.Run("too many recipients", func(t *testing.T) {
		customLimits := MessageLimits{
			MaxRecipientCount: 2,
		}
		err := ValidateRecipients([]string{"user1", "user2", "user3"}, customLimits)
		if !errors.Is(err, ErrTooManyRecipients) {
			t.Errorf("expected ErrTooManyRecipients, got %v", err)
		}
	})
}

// mockAttachment implements store.Attachment for testing.
type mockAttachment struct {
	id          string
	filename    string
	contentType string
	size        int64
}

func (a *mockAttachment) GetID() string           { return a.id }
func (a *mockAttachment) GetFilename() string     { return a.filename }
func (a *mockAttachment) GetContentType() string  { return a.contentType }
func (a *mockAttachment) GetSize() int64          { return a.size }
func (a *mockAttachment) GetURI() string          { return "" }
func (a *mockAttachment) GetCreatedAt() time.Time { return time.Time{} }

func TestValidateAttachments(t *testing.T) {
	limits := DefaultLimits()

	t.Run("valid attachments", func(t *testing.T) {
		attachments := []store.Attachment{
			&mockAttachment{id: "1", filename: "file1.txt", contentType: "text/plain", size: 1000},
			&mockAttachment{id: "2", filename: "file2.pdf", contentType: "application/pdf", size: 2000},
		}
		err := ValidateAttachments(attachments, limits)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty attachments is valid", func(t *testing.T) {
		err := ValidateAttachments([]store.Attachment{}, limits)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("nil attachments is valid", func(t *testing.T) {
		err := ValidateAttachments(nil, limits)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("too many attachments", func(t *testing.T) {
		customLimits := MessageLimits{
			MaxAttachmentCount: 2,
			MaxAttachmentSize:  100000,
		}
		attachments := []store.Attachment{
			&mockAttachment{id: "1", filename: "file1.txt", contentType: "text/plain", size: 100},
			&mockAttachment{id: "2", filename: "file2.txt", contentType: "text/plain", size: 100},
			&mockAttachment{id: "3", filename: "file3.txt", contentType: "text/plain", size: 100},
		}
		err := ValidateAttachments(attachments, customLimits)
		if !errors.Is(err, ErrTooManyAttachments) {
			t.Errorf("expected ErrTooManyAttachments, got %v", err)
		}
	})

	t.Run("attachment too large", func(t *testing.T) {
		customLimits := MessageLimits{
			MaxAttachmentCount: 10,
			MaxAttachmentSize:  1000,
		}
		attachments := []store.Attachment{
			&mockAttachment{id: "1", filename: "big.zip", contentType: "application/zip", size: 2000},
		}
		err := ValidateAttachments(attachments, customLimits)
		if !errors.Is(err, ErrAttachmentTooLarge) {
			t.Errorf("expected ErrAttachmentTooLarge, got %v", err)
		}
	})

	t.Run("empty filename", func(t *testing.T) {
		attachments := []store.Attachment{
			&mockAttachment{id: "1", filename: "", contentType: "text/plain", size: 100},
		}
		err := ValidateAttachments(attachments, limits)
		if !errors.Is(err, ErrInvalidAttachment) {
			t.Errorf("expected ErrInvalidAttachment, got %v", err)
		}
	})

	t.Run("empty content type", func(t *testing.T) {
		attachments := []store.Attachment{
			&mockAttachment{id: "1", filename: "file.txt", contentType: "", size: 100},
		}
		err := ValidateAttachments(attachments, limits)
		if !errors.Is(err, ErrInvalidMIMEType) {
			t.Errorf("expected ErrInvalidMIMEType, got %v", err)
		}
	})
}

func TestValidateMessageContent(t *testing.T) {
	t.Run("valid content", func(t *testing.T) {
		err := ValidateMessageContent("Hello", "This is a test message")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid subject", func(t *testing.T) {
		err := ValidateMessageContent("", "Body")
		if !errors.Is(err, ErrEmptySubject) {
			t.Errorf("expected ErrEmptySubject, got %v", err)
		}
	})

	t.Run("invalid body", func(t *testing.T) {
		err := ValidateMessageContent("Subject", "Body\x00with null")
		if !errors.Is(err, ErrInvalidContent) {
			t.Errorf("expected ErrInvalidContent, got %v", err)
		}
	})
}

func TestValidateMIMEType(t *testing.T) {
	t.Run("valid MIME types", func(t *testing.T) {
		tests := []string{
			"text/plain",
			"image/png",
			"application/pdf",
			"application/json",
			"text/plain; charset=utf-8",
			"image/jpeg; quality=80",
		}

		for _, ct := range tests {
			err := ValidateMIMEType(ct, nil, nil)
			if err != nil {
				t.Errorf("ValidateMIMEType(%q) unexpected error: %v", ct, err)
			}
		}
	})

	t.Run("empty content type is invalid", func(t *testing.T) {
		err := ValidateMIMEType("", nil, nil)
		if err == nil {
			t.Error("expected error for empty content type")
		}
	})

	t.Run("blocked types are rejected", func(t *testing.T) {
		blocked := []string{"application/x-executable", "application/x-sh"}

		err := ValidateMIMEType("application/x-executable", nil, blocked)
		if err == nil {
			t.Error("expected error for blocked content type")
		}

		// Non-blocked type should pass
		err = ValidateMIMEType("text/plain", nil, blocked)
		if err != nil {
			t.Errorf("unexpected error for non-blocked type: %v", err)
		}
	})

	t.Run("allowed types only permits listed types", func(t *testing.T) {
		allowed := []string{"image/png", "image/jpeg", "text/plain"}

		// Allowed type should pass
		err := ValidateMIMEType("image/png", allowed, nil)
		if err != nil {
			t.Errorf("unexpected error for allowed type: %v", err)
		}

		// Non-allowed type should fail
		err = ValidateMIMEType("application/pdf", allowed, nil)
		if err == nil {
			t.Error("expected error for non-allowed content type")
		}
	})

	t.Run("wildcard patterns work", func(t *testing.T) {
		allowed := []string{"image/*"}

		tests := []struct {
			ct   string
			want bool
		}{
			{"image/png", true},
			{"image/jpeg", true},
			{"image/gif", true},
			{"text/plain", false},
			{"video/mp4", false},
		}

		for _, tt := range tests {
			err := ValidateMIMEType(tt.ct, allowed, nil)
			got := err == nil
			if got != tt.want {
				t.Errorf("ValidateMIMEType(%q) with image/* allowed = %v, want %v", tt.ct, got, tt.want)
			}
		}
	})

	t.Run("blocked takes precedence over allowed", func(t *testing.T) {
		allowed := []string{"application/*"}
		blocked := []string{"application/x-executable"}

		// Executable should be blocked even though application/* is allowed
		err := ValidateMIMEType("application/x-executable", allowed, blocked)
		if err == nil {
			t.Error("expected error: blocked should take precedence")
		}

		// Other application types should pass
		err = ValidateMIMEType("application/json", allowed, blocked)
		if err != nil {
			t.Errorf("unexpected error for non-blocked application type: %v", err)
		}
	})
}

func TestValidateAttachmentsWithMIME(t *testing.T) {
	limits := DefaultLimits()

	t.Run("blocks executable attachments", func(t *testing.T) {
		attachments := []store.Attachment{
			&mockAttachment{id: "1", filename: "malware.exe", contentType: "application/x-msdownload", size: 100},
		}

		blocked := DefaultBlockedMIMETypes()
		err := ValidateAttachmentsWithMIME(attachments, limits, nil, blocked)
		if !errors.Is(err, ErrInvalidMIMEType) {
			t.Errorf("expected ErrInvalidMIMEType, got %v", err)
		}
	})

	t.Run("allows safe attachments", func(t *testing.T) {
		attachments := []store.Attachment{
			&mockAttachment{id: "1", filename: "doc.pdf", contentType: "application/pdf", size: 100},
			&mockAttachment{id: "2", filename: "image.png", contentType: "image/png", size: 200},
		}

		allowed := SafeAttachmentMIMETypes()
		err := ValidateAttachmentsWithMIME(attachments, limits, allowed, nil)
		if err != nil {
			t.Errorf("unexpected error for safe attachments: %v", err)
		}
	})

	t.Run("handles content type with parameters", func(t *testing.T) {
		attachments := []store.Attachment{
			&mockAttachment{id: "1", filename: "doc.txt", contentType: "text/plain; charset=utf-8", size: 100},
		}

		allowed := []string{"text/plain"}
		err := ValidateAttachmentsWithMIME(attachments, limits, allowed, nil)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestDefaultBlockedMIMETypes(t *testing.T) {
	blocked := DefaultBlockedMIMETypes()
	if len(blocked) == 0 {
		t.Error("expected non-empty blocked MIME types list")
	}

	// Should contain common executable types
	found := false
	for _, b := range blocked {
		if b == "application/x-msdownload" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected application/x-msdownload in blocked types")
	}
}

func TestSafeAttachmentMIMETypes(t *testing.T) {
	safe := SafeAttachmentMIMETypes()
	if len(safe) == 0 {
		t.Error("expected non-empty safe MIME types list")
	}

	// Should contain common document types
	hasImage := false
	hasPDF := false
	for _, s := range safe {
		if s == "image/*" {
			hasImage = true
		}
		if s == "application/pdf" {
			hasPDF = true
		}
	}
	if !hasImage {
		t.Error("expected image/* in safe types")
	}
	if !hasPDF {
		t.Error("expected application/pdf in safe types")
	}
}

func TestDefaultLimits(t *testing.T) {
	limits := DefaultLimits()

	if limits.MaxSubjectLength != DefaultMaxSubjectLength {
		t.Errorf("expected MaxSubjectLength %d, got %d", DefaultMaxSubjectLength, limits.MaxSubjectLength)
	}
	if limits.MaxBodySize != DefaultMaxBodySize {
		t.Errorf("expected MaxBodySize %d, got %d", DefaultMaxBodySize, limits.MaxBodySize)
	}
	if limits.MaxAttachmentSize != DefaultMaxAttachmentSize {
		t.Errorf("expected MaxAttachmentSize %d, got %d", DefaultMaxAttachmentSize, limits.MaxAttachmentSize)
	}
	if limits.MaxAttachmentCount != DefaultMaxAttachmentCount {
		t.Errorf("expected MaxAttachmentCount %d, got %d", DefaultMaxAttachmentCount, limits.MaxAttachmentCount)
	}
	if limits.MaxRecipientCount != DefaultMaxRecipientCount {
		t.Errorf("expected MaxRecipientCount %d, got %d", DefaultMaxRecipientCount, limits.MaxRecipientCount)
	}
	if limits.MaxMetadataSize != DefaultMaxMetadataSize {
		t.Errorf("expected MaxMetadataSize %d, got %d", DefaultMaxMetadataSize, limits.MaxMetadataSize)
	}
	if limits.MaxMetadataKeys != DefaultMaxMetadataKeys {
		t.Errorf("expected MaxMetadataKeys %d, got %d", DefaultMaxMetadataKeys, limits.MaxMetadataKeys)
	}
}
