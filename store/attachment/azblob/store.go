package azblob

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/google/uuid"
	"github.com/rbaliyan/mailbox/store"
)

// Store implements store.AttachmentFileStore using Azure Blob Storage.
type Store struct {
	client        *azblob.Client
	containerName string
	prefix        string
	logger        *slog.Logger
}

// Ensure Store implements AttachmentFileStore.
var _ store.AttachmentFileStore = (*Store)(nil)

// New creates a new Azure Blob Storage attachment store.
func New(opts ...Option) (*Store, error) {
	o := &options{
		prefix: "attachments",
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.containerName == "" {
		return nil, fmt.Errorf("container name is required")
	}

	// Warn if more than one authentication method is configured. buildClient
	// selects the first match in order (connection string > shared key > SAS),
	// so callers supplying multiple credentials should know which one wins.
	warnIfConflictingAuth(o)

	client, err := buildClient(o)
	if err != nil {
		return nil, fmt.Errorf("build azure blob client: %w", err)
	}

	return &Store{
		client:        client,
		containerName: o.containerName,
		prefix:        o.prefix,
		logger:        o.logger,
	}, nil
}

// warnIfConflictingAuth logs a warning when the caller has configured more
// than one authentication method. Precedence follows buildClient's switch
// order: connection string > shared key > SAS token.
func warnIfConflictingAuth(o *options) {
	configured := 0
	if o.connectionString != "" {
		configured++
	}
	if o.accountName != "" && o.accountKey != "" {
		configured++
	}
	if o.accountName != "" && o.sasToken != "" {
		configured++
	}
	if configured <= 1 {
		return
	}

	var winner string
	switch {
	case o.connectionString != "":
		winner = "connection string"
	case o.accountKey != "":
		winner = "shared key credential"
	default:
		winner = "SAS token"
	}
	o.logger.Warn("multiple azure blob authentication methods configured; using "+winner,
		"has_connection_string", o.connectionString != "",
		"has_shared_key", o.accountName != "" && o.accountKey != "",
		"has_sas_token", o.accountName != "" && o.sasToken != "",
	)
}

// buildClient constructs an azblob.Client from the configured credentials.
func buildClient(o *options) (*azblob.Client, error) {
	switch {
	case o.connectionString != "":
		return azblob.NewClientFromConnectionString(o.connectionString, nil)

	case o.accountName != "" && o.accountKey != "":
		cred, err := azblob.NewSharedKeyCredential(o.accountName, o.accountKey)
		if err != nil {
			return nil, fmt.Errorf("create shared key credential: %w", err)
		}
		serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net", o.accountName)
		return azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)

	case o.accountName != "" && o.sasToken != "":
		serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/?%s", o.accountName, o.sasToken)
		return azblob.NewClientWithNoCredential(serviceURL, nil)

	default:
		return nil, fmt.Errorf("one of connection string, shared key credential, or SAS token is required")
	}
}

// Upload uploads content to Azure Blob Storage and returns an az:// URI.
func (s *Store) Upload(ctx context.Context, filename, _ string, content io.Reader) (string, error) {
	key := s.generateKey(filename)

	_, err := s.client.UploadStream(ctx, s.containerName, key, content, nil)
	if err != nil {
		return "", fmt.Errorf("upload to azure blob: %w", err)
	}

	s.logger.Debug("uploaded attachment to azure blob", "container", s.containerName, "key", key)
	return fmt.Sprintf("az://%s/%s", s.containerName, key), nil
}

// Load returns a reader for the attachment content.
func (s *Store) Load(ctx context.Context, uri string) (io.ReadCloser, error) {
	container, key, err := parseAzureURI(uri)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.DownloadStream(ctx, container, key, nil)
	if err != nil {
		return nil, fmt.Errorf("download from azure blob: %w", err)
	}

	return resp.Body, nil
}

// Delete removes the attachment from Azure Blob Storage.
func (s *Store) Delete(ctx context.Context, uri string) error {
	container, key, err := parseAzureURI(uri)
	if err != nil {
		return err
	}

	_, err = s.client.DeleteBlob(ctx, container, key, nil)
	if err != nil {
		return fmt.Errorf("delete from azure blob: %w", err)
	}

	s.logger.Debug("deleted attachment from azure blob", "container", container, "key", key)
	return nil
}

// generateKey creates a unique blob key for the attachment.
func (s *Store) generateKey(filename string) string {
	now := time.Now().UTC()
	id := uuid.New().String()
	return path.Join(s.prefix, now.Format("2006/01/02"), id, filename)
}

// parseAzureURI parses an az:// URI into container name and blob key.
func parseAzureURI(uri string) (container, key string, err error) {
	if len(uri) < 6 || uri[:5] != "az://" {
		return "", "", fmt.Errorf("invalid azure blob uri: %s", uri)
	}

	rest := uri[5:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[:i], rest[i+1:], nil
		}
	}

	return "", "", fmt.Errorf("invalid azure blob uri (no key): %s", uri)
}
