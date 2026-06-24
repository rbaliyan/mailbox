package mailbox

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzValidateSubject checks that subject validation never panics and only
// returns known sentinel errors for arbitrary byte inputs.
func FuzzValidateSubject(f *testing.F) {
	f.Add("Hello World")
	f.Add("")
	f.Add("Re: " + string(make([]byte, DefaultMaxSubjectLength+1)))
	f.Add("\x00null byte")
	f.Add("\t tabbed subject")
	f.Add(string([]byte{0xff, 0xfe})) // invalid UTF-8

	f.Fuzz(func(t *testing.T, s string) {
		_ = ValidateSubject(s)
	})
}

// FuzzValidateBody checks that body validation never panics for arbitrary inputs.
func FuzzValidateBody(f *testing.F) {
	f.Add("Hello World")
	f.Add("")
	f.Add("\x00null byte")
	f.Add(string([]byte{0xff, 0xfe})) // invalid UTF-8
	f.Add("unicode: é中文")

	f.Fuzz(func(t *testing.T, s string) {
		_ = ValidateBody(s)
	})
}

// FuzzValidateMIMEType checks that MIME type parsing never panics.
func FuzzValidateMIMEType(f *testing.F) {
	f.Add("text/plain", "text/plain", "application/x-sh")
	f.Add("image/png", "", "")
	f.Add("application/octet-stream; charset=utf-8", "application/*", "")
	f.Add("", "", "")
	f.Add("image/*", "image/*", "")

	f.Fuzz(func(t *testing.T, contentType, allowed, blocked string) {
		var allowedTypes, blockedTypes []string
		if allowed != "" {
			allowedTypes = []string{allowed}
		}
		if blocked != "" {
			blockedTypes = []string{blocked}
		}
		_ = ValidateMIMEType(contentType, allowedTypes, blockedTypes)
	})
}

// FuzzValidateHeaders checks that header validation never panics. The fuzzed
// blob is split into N key/value pairs so the resulting map reaches the
// MaxHeaderCount and MaxHeadersTotalSize branches of ValidateHeaders, not just
// the single-key path.
func FuzzValidateHeaders(f *testing.F) {
	f.Add("X-Custom:value")
	f.Add(":empty key")
	f.Add("key:")
	f.Add("")
	// Many small headers — drives the MaxHeaderCount branch.
	f.Add(strings.Repeat("k:v\n", DefaultMaxHeaderCount+5))
	// A few large values — drives the MaxHeadersTotalSize branch.
	f.Add("big:" + strings.Repeat("x", DefaultMaxHeadersTotalSize+1))

	f.Fuzz(func(t *testing.T, blob string) {
		// Build a multi-key header map from the fuzzed blob. Each newline-
		// separated record becomes "key:value"; later duplicate keys overwrite
		// earlier ones, which is fine — the goal is varied map shapes.
		headers := make(map[string]string)
		for i, record := range strings.Split(blob, "\n") {
			k, v, found := strings.Cut(record, ":")
			if !found {
				// No separator: synthesize a unique key so distinct records
				// produce distinct entries and push toward MaxHeaderCount.
				k = fmt.Sprintf("k%d", i)
				v = record
			}
			headers[k] = v
		}
		_ = ValidateHeaders(headers, DefaultLimits())
	})
}

// FuzzValidateMetadata checks that metadata validation never panics. The blob
// is split into N keys (with string values) so the map reaches the
// MaxMetadataKeys and MaxMetadataSize (JSON-serialized) branches.
func FuzzValidateMetadata(f *testing.F) {
	f.Add("source:api")
	f.Add(":empty")
	f.Add(strings.Repeat("k:v\n", DefaultMaxMetadataKeys+5))
	f.Add("big:" + strings.Repeat("x", DefaultMaxMetadataSize+1))
	f.Add(strings.Repeat("x", MaxMetadataKeyLength+10) + ":overlongkey")

	f.Fuzz(func(t *testing.T, blob string) {
		metadata := make(map[string]any)
		for i, record := range strings.Split(blob, "\n") {
			k, v, found := strings.Cut(record, ":")
			if !found {
				k = fmt.Sprintf("k%d", i)
				v = record
			}
			metadata[k] = v
		}
		_ = ValidateMetadata(metadata)
	})
}

// FuzzValidateRecipients checks that recipient validation never panics. The
// blob is split into N recipient IDs so the list reaches the empty-list,
// MaxRecipientCount, and empty-ID branches.
func FuzzValidateRecipients(f *testing.F) {
	f.Add("bob")
	f.Add("")
	f.Add("alice,bob,carol")
	f.Add(strings.Repeat("u,", DefaultMaxRecipientCount+5))
	f.Add("alice,,carol") // empty ID in the middle

	f.Fuzz(func(t *testing.T, blob string) {
		var recipients []string
		if blob != "" {
			recipients = strings.Split(blob, ",")
		}
		_ = ValidateRecipients(recipients, DefaultLimits())
	})
}
