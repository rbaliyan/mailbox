# File Attachments

Mailbox supports file attachments via four backends. All implement `store.AttachmentFileStore`:

```go
type AttachmentFileStore interface {
    Upload(ctx context.Context, filename, contentType string, content io.Reader) (string, error)
    Load(ctx context.Context, uri string) (io.ReadCloser, error)
    Delete(ctx context.Context, uri string) error
}
```

Wire a backend into the service via `mailbox.WithAttachmentManager`:

```go
import "github.com/rbaliyan/mailbox/store/attachment"

// Wrap a file store with reference counting / deduplication
manager := attachment.NewManager(metadataStore, fileStore)

svc, _ := mailbox.New(mailbox.Config{},
    mailbox.WithStore(store),
    mailbox.WithAttachmentManager(manager),
)
```

## S3

```go
import (
    "github.com/aws/aws-sdk-go-v2/config"
    s3store "github.com/rbaliyan/mailbox/store/attachment/s3"
)

cfg, _ := config.LoadDefaultConfig(ctx)

fileStore, err := s3store.New(ctx,
    s3store.WithBucket("my-attachments"),
    s3store.WithPrefix("mailbox/"),
    s3store.WithConfig(cfg),
)
```

## GCS

```go
import (
    gcsstore "github.com/rbaliyan/mailbox/store/attachment/gcs"
)

fileStore, err := gcsstore.New(ctx,
    gcsstore.WithBucket("my-attachments"),
    gcsstore.WithPrefix("mailbox/"),
)
```

## Azure Blob Storage

```go
import azblob "github.com/rbaliyan/mailbox/store/attachment/azblob"

// Connection string (simplest)
fileStore, err := azblob.New(
    azblob.WithContainer("my-attachments"),
    azblob.WithConnectionString(os.Getenv("AZURE_STORAGE_CONNECTION_STRING")),
)

// Shared key credential
fileStore, err = azblob.New(
    azblob.WithContainer("my-attachments"),
    azblob.WithSharedKeyCredential(accountName, accountKey),
)

// SAS token
fileStore, err = azblob.New(
    azblob.WithContainer("my-attachments"),
    azblob.WithSASToken(accountName, sasToken),
)
```

Uploaded blobs are stored at `attachments/YYYY/MM/DD/<uuid>/<filename>` and returned as `az://<container>/<key>` URIs.

## Local Filesystem

Useful for development and single-host deployments:

```go
import localstore "github.com/rbaliyan/mailbox/store/attachment/local"

fileStore, err := localstore.New(
    localstore.WithBaseDir("/var/mailbox/attachments"),
    localstore.WithBaseURL("https://files.example.com"), // optional: serve via HTTP
)

// Optionally mount the file server in your HTTP mux:
http.Handle("/files/", http.StripPrefix("/files", fileStore.Handler()))
```

Files are stored at `<baseDir>/YYYY/MM/DD/<uuid>/<filename>` and returned as `file:///abs/path` URIs (or `http://baseURL/...` when a base URL is set).
