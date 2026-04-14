package compress_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"testing"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/compress"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

func init() {
	log.SetOutput(io.Discard)
}

func TestCompressionPlugin_RoundTrip(t *testing.T) {
	ctx := context.Background()

	svc, err := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(compress.NewPlugin(compress.Gzip)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer svc.Close(ctx)

	original := "Hello, this is a test message that should be compressed!"
	alice := svc.Client("alice")
	msg, err := alice.SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Compressed",
		Body:         original,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if msg.GetBody() == original {
		t.Error("body should be compressed, not plaintext")
	}
	if msg.GetHeaders()[store.HeaderContentEncoding] != "gzip" {
		t.Errorf("expected Content-Encoding: gzip, got %q", msg.GetHeaders()[store.HeaderContentEncoding])
	}

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	plaintext, err := compress.Open(inbox.All()[0])
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plaintext) != original {
		t.Errorf("expected %q, got %q", original, string(plaintext))
	}
}

func TestDecompress_Gzip(t *testing.T) {
	original := []byte("test data for compression round-trip")
	compressed, err := compress.Gzip.Compress(original)
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := compress.Decompress(compressed, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decompressed, original) {
		t.Errorf("round-trip failed")
	}
}

func TestDecompress_UnsupportedEncoding(t *testing.T) {
	_, err := compress.Decompress([]byte("data"), "brotli")
	if err == nil {
		t.Fatal("expected error for unsupported encoding")
	}
}

func TestIsCompressed(t *testing.T) {
	ctx := context.Background()

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(compress.NewPlugin(compress.Gzip)),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "test",
		Body:         "body",
	})
	inbox, _ := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	if !compress.IsCompressed(inbox.All()[0]) {
		t.Error("should be compressed")
	}
}

// ExampleNewPlugin_basic demonstrates basic compression and decompression.
func ExampleNewPlugin_basic() {
	ctx := context.Background()

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(compress.NewPlugin(compress.Gzip)),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Compressed",
		Body:         "This body is gzip compressed before storage.",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]

	fmt.Println("encoding:", msg.GetHeaders()["Content-Encoding"])
	plaintext, _ := compress.Open(msg)
	fmt.Println("body:", string(plaintext))
	// Output:
	// encoding: gzip
	// body: This body is gzip compressed before storage.
}

// ExampleNewPlugin_bestCompression demonstrates using best compression level.
func ExampleNewPlugin_bestCompression() {
	ctx := context.Background()

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(compress.NewPlugin(compress.GzipBest)),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Best compression",
		Body:         "Repetitive data benefits from best compression: aaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	plaintext, _ := compress.Open(inbox.All()[0])
	fmt.Println("body:", string(plaintext))
	// Output:
	// body: Repetitive data benefits from best compression: aaaaaaaaaaaaaaaaaaaaaaaaaaa
}

// ExampleOpen demonstrates decompressing a message retrieved from a mailbox.
func ExampleOpen() {
	ctx := context.Background()

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(compress.NewPlugin(compress.Gzip)),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	// Send a compressed message.
	svc.Client("system").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "System notification",
		Body:         "<h1>Alert</h1><p>Disk usage at 90%.</p>",
		Headers:      map[string]string{"Content-Type": "text/html"},
	})

	// Read and decompress.
	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]

	fmt.Println("compressed:", compress.IsCompressed(msg))
	fmt.Println("subject:", msg.GetSubject())

	body, _ := compress.Open(msg)
	fmt.Println("body:", string(body))
	// Output:
	// compressed: true
	// subject: System notification
	// body: <h1>Alert</h1><p>Disk usage at 90%.</p>
}

// ExampleOpen_uncompressed demonstrates that Open works transparently on plain messages.
func ExampleOpen_uncompressed() {
	ctx := context.Background()

	// No compression plugin.
	svc, _ := mailbox.New(mailbox.Config{}, mailbox.WithStore(memory.New()))
	svc.Connect(ctx)
	defer svc.Close(ctx)

	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "Plain text",
		Body:         "No compression here.",
	})

	bob := svc.Client("bob")
	inbox, _ := bob.Folder(ctx, store.FolderInbox, store.ListOptions{})
	msg := inbox.All()[0]

	fmt.Println("compressed:", compress.IsCompressed(msg))
	body, _ := compress.Open(msg)
	fmt.Println("body:", string(body))
	// Output:
	// compressed: false
	// body: No compression here.
}

// ExampleIsCompressed demonstrates checking if a message is compressed.
func ExampleIsCompressed() {
	ctx := context.Background()

	svc, _ := mailbox.New(mailbox.Config{},
		mailbox.WithStore(memory.New()),
		mailbox.WithPlugins(compress.NewPlugin(compress.Gzip)),
	)
	svc.Connect(ctx)
	defer svc.Close(ctx)

	svc.Client("alice").SendMessage(ctx, mailbox.SendRequest{
		RecipientIDs: []string{"bob"},
		Subject:      "test",
		Body:         "body",
	})

	inbox, _ := svc.Client("bob").Folder(ctx, store.FolderInbox, store.ListOptions{})
	fmt.Println("compressed:", compress.IsCompressed(inbox.All()[0]))
	// Output:
	// compressed: true
}
