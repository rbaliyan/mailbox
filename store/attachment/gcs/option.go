package gcs

import (
	"log/slog"
)

// options holds GCS store configuration.
type options struct {
	// Bucket configuration
	bucket string
	prefix string

	// Custom endpoint (for emulators, testing)
	endpoint string

	// Credentials options (mutually exclusive)
	credentialsJSON []byte  // Service account JSON key
	credentialsFile string  // Path to service account JSON file
	apiKey          string  // API key (not recommended)

	// Logger
	logger *slog.Logger
}

// Option configures the GCS store.
type Option func(*options)

// WithBucket sets the GCS bucket name (required).
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

// WithEndpoint sets a custom GCS endpoint (for emulators, testing).
func WithEndpoint(endpoint string) Option {
	return func(o *options) {
		o.endpoint = endpoint
	}
}

// WithCredentialsJSON sets service account credentials from JSON bytes.
// Use this when you have the service account key loaded in memory.
//
// Example:
//
//	keyJSON, _ := os.ReadFile("service-account.json")
//	store, _ := gcs.New(ctx, gcs.WithBucket("my-bucket"), gcs.WithCredentialsJSON(keyJSON))
func WithCredentialsJSON(json []byte) Option {
	return func(o *options) {
		o.credentialsJSON = json
	}
}

// WithCredentialsFile sets the path to a service account JSON key file.
// This is equivalent to setting GOOGLE_APPLICATION_CREDENTIALS environment variable.
//
// Example:
//
//	store, _ := gcs.New(ctx, gcs.WithBucket("my-bucket"), gcs.WithCredentialsFile("/path/to/sa.json"))
func WithCredentialsFile(path string) Option {
	return func(o *options) {
		o.credentialsFile = path
	}
}

// WithAPIKey sets an API key for authentication.
// Note: API keys have limited functionality and are not recommended for production.
// Prefer service accounts or Workload Identity.
func WithAPIKey(key string) Option {
	return func(o *options) {
		o.apiKey = key
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

// WithWorkloadIdentity is a no-op option that documents Workload Identity usage.
// Workload Identity is the recommended way to provide GCP credentials to pods
// running in GKE (Google Kubernetes Engine).
//
// Setup:
//  1. Enable Workload Identity on your GKE cluster
//  2. Create a GCP service account with Storage permissions
//  3. Create an IAM policy binding:
//     gcloud iam service-accounts add-iam-policy-binding GSA_NAME@PROJECT_ID.iam.gserviceaccount.com \
//     --role roles/iam.workloadIdentityUser \
//     --member "serviceAccount:PROJECT_ID.svc.id.goog[NAMESPACE/KSA_NAME]"
//  4. Annotate your Kubernetes service account:
//     apiVersion: v1
//     kind: ServiceAccount
//     metadata:
//     name: my-app
//     annotations:
//     iam.gke.io/gcp-service-account: GSA_NAME@PROJECT_ID.iam.gserviceaccount.com
//  5. Use the service account in your pod spec
//
// The GCP SDK automatically detects Workload Identity via the GKE metadata server.
// No explicit configuration is needed - just don't set any credential options.
func WithWorkloadIdentity() Option {
	return func(o *options) {
		// No-op - SDK handles Workload Identity automatically via ADC
	}
}

// WithApplicationDefaultCredentials is a no-op option that documents ADC usage.
// Application Default Credentials (ADC) is the recommended credential discovery mechanism.
// When no credential options are set, the GCP SDK uses ADC which checks:
//
//  1. GOOGLE_APPLICATION_CREDENTIALS environment variable
//  2. User credentials from gcloud auth application-default login
//  3. Workload Identity (on GKE)
//  4. Compute Engine default service account
//  5. Cloud Run / Cloud Functions default service account
//  6. App Engine default service account
//
// This is the recommended approach for most deployments.
func WithApplicationDefaultCredentials() Option {
	return func(o *options) {
		// No-op - SDK uses ADC automatically
	}
}
