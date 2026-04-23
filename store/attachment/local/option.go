// Package local provides a local filesystem-based attachment file store.
// It is intended for development, testing, and on-premises deployments.
package local

import (
	"log/slog"
)

// options holds local filesystem store configuration.
type options struct {
	// baseDir is the root directory for attachment storage (required).
	baseDir string

	// baseURL is the public URL prefix used for GenerateURL-style access.
	// When non-empty, Upload returns an "http[s]://..." URI instead of "file://...".
	// Example: "http://localhost:8080/attachments"
	baseURL string

	logger *slog.Logger
}

// Option configures the local filesystem store.
type Option func(*options)

// WithBaseDir sets the root directory for attachment storage (required).
func WithBaseDir(dir string) Option {
	return func(o *options) {
		o.baseDir = dir
	}
}

// WithBaseURL sets the public HTTP base URL for generated URIs.
// When set, Upload returns "http[s]://baseURL/path" instead of "file:///absPath".
// Use Handler to mount the filesystem at this URL path in your HTTP server.
func WithBaseURL(url string) Option {
	return func(o *options) {
		o.baseURL = url
	}
}

// WithLogger sets a custom logger.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		if logger != nil {
			o.logger = logger
		}
	}
}
