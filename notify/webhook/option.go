// Package webhook provides a notify.Router that delivers notification events
// to configured HTTP endpoints via signed JSON POST requests.
package webhook

import (
	"log/slog"
	"time"
)

const (
	defaultTimeout    = 10 * time.Second
	defaultMaxRetries = 3
	defaultInitDelay  = 100 * time.Millisecond
	defaultMaxDelay   = 30 * time.Second
)

// EndpointConfig describes a single webhook destination.
type EndpointConfig struct {
	// URL is the HTTP endpoint that receives POST requests.
	URL string

	// Headers are additional HTTP headers sent with every request to this endpoint.
	Headers map[string]string

	// Events restricts delivery to the listed event types.
	// When empty, all event types are delivered.
	Events []string
}

// options holds Router configuration.
type options struct {
	endpoints   []EndpointConfig
	timeout     time.Duration
	maxRetries  int
	initDelay   time.Duration
	maxDelay    time.Duration
	signingKey  []byte
	logger      *slog.Logger
}

// Option configures a webhook Router.
type Option func(*options)

// WithEndpoints sets the webhook destinations.
func WithEndpoints(endpoints ...EndpointConfig) Option {
	return func(o *options) {
		o.endpoints = append(o.endpoints, endpoints...)
	}
}

// WithTimeout sets the per-request HTTP timeout. Default is 10s.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// WithMaxRetries sets the maximum number of delivery attempts per endpoint.
// Default is 3 (one initial attempt plus two retries).
func WithMaxRetries(n int) Option {
	return func(o *options) {
		if n >= 0 {
			o.maxRetries = n
		}
	}
}

// WithSigningKey sets the HMAC-SHA256 signing secret.
// When set, each request includes X-Mailbox-Signature and X-Mailbox-Timestamp headers.
// Use VerifySignature on the receiving end to authenticate the request.
func WithSigningKey(key []byte) Option {
	return func(o *options) {
		o.signingKey = key
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
