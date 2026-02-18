# File Attachments

Mailbox supports file attachments via S3 and GCS backends with reference counting and deduplication.

## S3

```go
import (
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/s3"
    s3store "github.com/rbaliyan/mailbox/store/attachment/s3"
)

cfg, _ := config.LoadDefaultConfig(ctx)
s3Client := s3.NewFromConfig(cfg)

attachmentStore := s3store.New(s3Client,
    s3store.WithBucket("my-attachments"),
    s3store.WithPrefix("mailbox/"),
)

svc, _ := mailbox.NewService(
    mailbox.WithStore(store),
    mailbox.WithAttachmentStore(attachmentStore),
)
```

## GCS

```go
import (
    "cloud.google.com/go/storage"
    gcsstore "github.com/rbaliyan/mailbox/store/attachment/gcs"
)

gcsClient, _ := storage.NewClient(ctx)

attachmentStore := gcsstore.New(gcsClient,
    gcsstore.WithBucket("my-attachments"),
    gcsstore.WithPrefix("mailbox/"),
)
```
