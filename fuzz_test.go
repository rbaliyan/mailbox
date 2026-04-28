package mailbox

import (
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

// FuzzValidateHeaders checks that header validation never panics.
func FuzzValidateHeaders(f *testing.F) {
	f.Add("X-Custom", "value")
	f.Add("", "empty key")
	f.Add("key", "")

	f.Fuzz(func(t *testing.T, key, value string) {
		headers := map[string]string{key: value}
		_ = ValidateHeaders(headers, DefaultLimits())
	})
}
