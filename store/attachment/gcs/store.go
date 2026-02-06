// Package gcs provides a Google Cloud Storage-based attachment file store.
package gcs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/rbaliyan/mailbox/store"
	"google.golang.org/api/option"
)

// Store implements store.AttachmentFileStore using Google Cloud Storage.
type Store struct {
	client *storage.Client
	bucket string
	prefix string
	logger *slog.Logger
}

// Ensure Store implements AttachmentFileStore.
var _ store.AttachmentFileStore = (*Store)(nil)

// New creates a new GCS attachment store.
func New(ctx context.Context, opts ...Option) (*Store, error) {
	o := &options{
		prefix: "attachments",
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.bucket == "" {
		return nil, fmt.Errorf("bucket is required")
	}

	// Build client options
	clientOpts, err := buildClientOptions(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("build client options: %w", err)
	}

	client, err := storage.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("create gcs client: %w", err)
	}

	return &Store{
		client: client,
		bucket: o.bucket,
		prefix: o.prefix,
		logger: o.logger,
	}, nil
}

// buildClientOptions builds GCS client options based on authentication settings.
func buildClientOptions(_ context.Context, o *options) ([]option.ClientOption, error) {
	var opts []option.ClientOption

	switch {
	case o.credentialsJSON != nil:
		// Use provided JSON credentials (service account key)
		creds, err := credentials.DetectDefault(&credentials.DetectOptions{
			Scopes:          []string{"https://www.googleapis.com/auth/cloud-platform"},
			CredentialsJSON: o.credentialsJSON,
		})
		if err != nil {
			return nil, fmt.Errorf("detect credentials from json: %w", err)
		}
		opts = append(opts, option.WithAuthCredentials(creds))

	case o.credentialsFile != "":
		// Use credentials from file
		creds, err := credentials.DetectDefault(&credentials.DetectOptions{
			Scopes:          []string{"https://www.googleapis.com/auth/cloud-platform"},
			CredentialsFile: o.credentialsFile,
		})
		if err != nil {
			return nil, fmt.Errorf("detect credentials from file: %w", err)
		}
		opts = append(opts, option.WithAuthCredentials(creds))

	case o.apiKey != "":
		// Use API key (limited functionality, not recommended for production)
		opts = append(opts, option.WithAPIKey(o.apiKey))

	default:
		// Use Application Default Credentials (ADC)
		// This handles:
		// - GOOGLE_APPLICATION_CREDENTIALS environment variable
		// - gcloud auth application-default login credentials
		// - Workload Identity on GKE (service account annotation)
		// - Compute Engine default service account
		// - Cloud Run/Cloud Functions default service account
		// No explicit options needed - SDK handles it automatically
	}

	// Add custom endpoint if specified (for emulators, testing)
	if o.endpoint != "" {
		opts = append(opts, option.WithEndpoint(o.endpoint))
	}

	return opts, nil
}

// Upload uploads content to GCS and returns the object path as URI.
func (s *Store) Upload(ctx context.Context, filename, contentType string, content io.Reader) (string, error) {
	// Generate unique key
	key := s.generateKey(filename)

	obj := s.client.Bucket(s.bucket).Object(key)
	w := obj.NewWriter(ctx)
	w.ContentType = contentType

	if _, err := io.Copy(w, content); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("copy content to gcs: %w", err)
	}

	if err := w.Close(); err != nil {
		return "", fmt.Errorf("close gcs writer: %w", err)
	}

	s.logger.Debug("uploaded attachment to gcs", "bucket", s.bucket, "key", key)

	// Return the key as URI (gs://bucket/key format)
	return fmt.Sprintf("gs://%s/%s", s.bucket, key), nil
}

// Load returns a reader for the attachment content.
func (s *Store) Load(ctx context.Context, uri string) (io.ReadCloser, error) {
	bucket, key, err := parseGCSURI(uri)
	if err != nil {
		return nil, err
	}

	obj := s.client.Bucket(bucket).Object(key)
	r, err := obj.NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("create gcs reader: %w", err)
	}

	return r, nil
}

// Delete removes the attachment from GCS.
func (s *Store) Delete(ctx context.Context, uri string) error {
	bucket, key, err := parseGCSURI(uri)
	if err != nil {
		return err
	}

	obj := s.client.Bucket(bucket).Object(key)
	if err := obj.Delete(ctx); err != nil {
		return fmt.Errorf("delete object from gcs: %w", err)
	}

	s.logger.Debug("deleted attachment from gcs", "bucket", bucket, "key", key)
	return nil
}

// Close closes the GCS client.
func (s *Store) Close() error {
	return s.client.Close()
}

// generateKey creates a unique GCS key for the attachment.
func (s *Store) generateKey(filename string) string {
	// Use date-based partitioning
	now := time.Now().UTC()
	id := uuid.New().String()
	return path.Join(s.prefix, now.Format("2006/01/02"), id, filename)
}

// parseGCSURI parses a gs:// URI into bucket and key.
func parseGCSURI(uri string) (bucket, key string, err error) {
	if len(uri) < 6 || uri[:5] != "gs://" {
		return "", "", fmt.Errorf("invalid gcs uri: %s", uri)
	}

	rest := uri[5:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[:i], rest[i+1:], nil
		}
	}

	return "", "", fmt.Errorf("invalid gcs uri (no key): %s", uri)
}
