package s3

import (
	"log/slog"
)

// options holds S3 store configuration.
type options struct {
	// Bucket configuration
	bucket string
	prefix string
	region string

	// Custom endpoint (for S3-compatible services like MinIO)
	endpoint     string
	usePathStyle bool

	// Static credentials (Access Key + Secret Key)
	accessKey    string
	secretKey    string
	sessionToken string

	// IAM Role-based access
	roleARN         string
	roleSessionName string
	externalID      string

	// Logger
	logger *slog.Logger
}

// Option configures the S3 store.
type Option func(*options)

// WithBucket sets the S3 bucket name (required).
func WithBucket(bucket string) Option {
	return func(o *options) {
		o.bucket = bucket
	}
}

// WithPrefix sets the key prefix for attachments.
// Default is "attachments".
func WithPrefix(prefix string) Option {
	return func(o *options) {
		o.prefix = prefix
	}
}

// WithRegion sets the AWS region.
// Default is "us-east-1".
func WithRegion(region string) Option {
	return func(o *options) {
		o.region = region
	}
}

// WithEndpoint sets a custom S3 endpoint for S3-compatible services (MinIO, LocalStack, etc.).
func WithEndpoint(endpoint string) Option {
	return func(o *options) {
		o.endpoint = endpoint
	}
}

// WithPathStyle enables path-style addressing (required for some S3-compatible services).
func WithPathStyle(enabled bool) Option {
	return func(o *options) {
		o.usePathStyle = enabled
	}
}

// WithStaticCredentials sets static AWS credentials (Access Key + Secret Key).
// Use this for programmatic access with long-term credentials.
// For Kubernetes, prefer IAM Roles for Service Accounts (IRSA) instead.
func WithStaticCredentials(accessKey, secretKey string) Option {
	return func(o *options) {
		o.accessKey = accessKey
		o.secretKey = secretKey
	}
}

// WithSessionToken sets an optional session token for temporary credentials.
// Use with WithStaticCredentials when using STS temporary credentials.
func WithSessionToken(token string) Option {
	return func(o *options) {
		o.sessionToken = token
	}
}

// WithAssumeRole configures IAM role assumption.
// The store will use STS AssumeRole to get temporary credentials.
// roleARN: The ARN of the role to assume (e.g., "arn:aws:iam::123456789012:role/MyRole")
// sessionName: A name for the assumed role session (optional, defaults to "mailbox-attachment-store")
func WithAssumeRole(roleARN, sessionName string) Option {
	return func(o *options) {
		o.roleARN = roleARN
		if sessionName != "" {
			o.roleSessionName = sessionName
		} else {
			o.roleSessionName = "mailbox-attachment-store"
		}
	}
}

// WithExternalID sets the external ID for role assumption.
// Used for cross-account access when the role requires an external ID.
func WithExternalID(externalID string) Option {
	return func(o *options) {
		o.externalID = externalID
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

// WithIRSA is a no-op option that documents IRSA usage.
// IAM Roles for Service Accounts (IRSA) is the recommended way to provide
// AWS credentials to pods running in EKS.
//
// Setup:
// 1. Create an IAM role with S3 permissions and trust policy for your service account
// 2. Annotate your Kubernetes service account:
//
//	apiVersion: v1
//	kind: ServiceAccount
//	metadata:
//	  name: my-app
//	  annotations:
//	    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/MyRole
//
// 3. Use the service account in your pod spec
//
// The AWS SDK automatically detects IRSA via the AWS_ROLE_ARN and
// AWS_WEB_IDENTITY_TOKEN_FILE environment variables injected by EKS.
// No explicit configuration is needed - just don't set any credential options.
func WithIRSA() Option {
	return func(o *options) {
		// No-op - SDK handles IRSA automatically
	}
}

// WithDefaultCredentialChain is a no-op option that documents the default credential chain.
// When no credential options are set, the AWS SDK uses the default credential chain:
//
// 1. Environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN)
// 2. Shared credentials file (~/.aws/credentials)
// 3. Shared config file (~/.aws/config)
// 4. IAM role for Amazon EC2 (via instance metadata)
// 5. IAM role for Amazon ECS tasks
// 6. IAM Roles for Service Accounts (IRSA) on EKS
//
// This is the recommended approach for most deployments.
func WithDefaultCredentialChain() Option {
	return func(o *options) {
		// No-op - SDK uses default chain automatically
	}
}
