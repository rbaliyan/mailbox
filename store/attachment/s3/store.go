// Package s3 provides an S3-based attachment file store.
package s3

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/rbaliyan/mailbox/store"
)

// Store implements store.AttachmentFileStore using AWS S3.
type Store struct {
	client *s3.Client
	tm     *transfermanager.Client
	bucket   string
	prefix   string
	logger   *slog.Logger
}

// Ensure Store implements AttachmentFileStore.
var _ store.AttachmentFileStore = (*Store)(nil)

// New creates a new S3 attachment store.
// The context is used for AWS credential loading and configuration.
func New(ctx context.Context, opts ...Option) (*Store, error) {
	o := &options{
		region: "us-east-1",
		prefix: "attachments",
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.bucket == "" {
		return nil, fmt.Errorf("bucket is required")
	}

	// Build AWS config using the provided context
	awsCfg, err := buildAWSConfig(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("build aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(opts *s3.Options) {
		if o.endpoint != "" {
			opts.BaseEndpoint = aws.String(o.endpoint)
			opts.UsePathStyle = o.usePathStyle
		}
	})

	return &Store{
		client: client,
		tm:     transfermanager.New(client),
		bucket: o.bucket,
		prefix: o.prefix,
		logger: o.logger,
	}, nil
}

// buildAWSConfig builds AWS config based on authentication options.
func buildAWSConfig(ctx context.Context, o *options) (aws.Config, error) {
	var optFns []func(*config.LoadOptions) error

	// Set region
	optFns = append(optFns, config.WithRegion(o.region))

	// Configure credentials based on auth method
	switch {
	case o.accessKey != "" && o.secretKey != "":
		// Static credentials (Access Key + Secret Key)
		creds := credentials.NewStaticCredentialsProvider(o.accessKey, o.secretKey, o.sessionToken)
		optFns = append(optFns, config.WithCredentialsProvider(creds))

	case o.roleARN != "":
		// IAM Role - use STS AssumeRole
		// First load default config, then assume role
		baseCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(o.region))
		if err != nil {
			return aws.Config{}, fmt.Errorf("load base config for role: %w", err)
		}

		// Use STS to assume role
		stsCreds := newAssumeRoleProvider(baseCfg, o.roleARN, o.roleSessionName, o.externalID)
		optFns = append(optFns, config.WithCredentialsProvider(stsCreds))

	default:
		// Default credential chain (IAM role on EC2/EKS, env vars, shared config, etc.)
		// This handles:
		// - Environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY)
		// - Shared credentials file (~/.aws/credentials)
		// - IAM role for EC2
		// - IAM role for EKS (via IRSA - IAM Roles for Service Accounts)
		// - ECS task role
		// No explicit credentials needed - SDK handles it automatically
	}

	return config.LoadDefaultConfig(ctx, optFns...)
}

// Upload uploads content to S3 and returns the object key as URI.
func (s *Store) Upload(ctx context.Context, filename, contentType string, content io.Reader) (string, error) {
	// Generate unique key
	key := s.generateKey(filename)

	input := &transfermanager.UploadObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        content,
		ContentType: aws.String(contentType),
	}

	_, err := s.tm.UploadObject(ctx, input)
	if err != nil {
		return "", fmt.Errorf("upload to s3: %w", err)
	}

	s.logger.Debug("uploaded attachment to s3", "bucket", s.bucket, "key", key)

	// Return the key as URI (s3://bucket/key format)
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

// Load returns a reader for the attachment content.
func (s *Store) Load(ctx context.Context, uri string) (io.ReadCloser, error) {
	bucket, key, err := parseS3URI(uri)
	if err != nil {
		return nil, err
	}

	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("get object from s3: %w", err)
	}

	return output.Body, nil
}

// Delete removes the attachment from S3.
func (s *Store) Delete(ctx context.Context, uri string) error {
	bucket, key, err := parseS3URI(uri)
	if err != nil {
		return err
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object from s3: %w", err)
	}

	s.logger.Debug("deleted attachment from s3", "bucket", bucket, "key", key)
	return nil
}

// generateKey creates a unique S3 key for the attachment.
func (s *Store) generateKey(filename string) string {
	// Use date-based partitioning for better S3 performance
	now := time.Now().UTC()
	id := uuid.New().String()
	return path.Join(s.prefix, now.Format("2006/01/02"), id, filename)
}

// parseS3URI parses an s3:// URI into bucket and key.
func parseS3URI(uri string) (bucket, key string, err error) {
	if len(uri) < 6 || uri[:5] != "s3://" {
		return "", "", fmt.Errorf("invalid s3 uri: %s", uri)
	}

	rest := uri[5:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[:i], rest[i+1:], nil
		}
	}

	return "", "", fmt.Errorf("invalid s3 uri (no key): %s", uri)
}
