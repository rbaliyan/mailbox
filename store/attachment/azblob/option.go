// Package azblob provides an Azure Blob Storage-based attachment file store.
package azblob

import (
	"log/slog"
)

// options holds Azure Blob store configuration.
type options struct {
	// Container configuration
	containerName string
	prefix        string

	// Authentication (mutually exclusive; connection string takes precedence)
	connectionString string
	accountName      string
	accountKey       string
	sasToken         string // SAS token appended to the service URL

	// Logger
	logger *slog.Logger
}

// Option configures the Azure Blob store.
type Option func(*options)

// WithContainer sets the Azure Blob container name (required).
func WithContainer(name string) Option {
	return func(o *options) {
		o.containerName = name
	}
}

// WithPrefix sets the blob key prefix for attachments.
// Default is "attachments".
func WithPrefix(prefix string) Option {
	return func(o *options) {
		o.prefix = prefix
	}
}

// WithConnectionString configures the store using an Azure Storage connection string.
// This is the simplest authentication method and takes precedence over all others.
func WithConnectionString(cs string) Option {
	return func(o *options) {
		o.connectionString = cs
	}
}

// WithSharedKeyCredential configures the store using an account name and key.
func WithSharedKeyCredential(accountName, accountKey string) Option {
	return func(o *options) {
		o.accountName = accountName
		o.accountKey = accountKey
	}
}

// WithSASToken configures the store using a Shared Access Signature token.
// Must be paired with WithAccountName so the service URL can be constructed.
// The token is appended to the service URL: https://<account>.blob.core.windows.net/?<token>
func WithSASToken(accountName, token string) Option {
	return func(o *options) {
		o.accountName = accountName
		o.sasToken = token
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
