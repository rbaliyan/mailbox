package local

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func newTestStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	dir := t.TempDir()
	opts = append([]Option{WithBaseDir(dir)}, opts...)
	s, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestUploadLoadDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	want := []byte("hello, world")
	uri, err := s.Upload(ctx, "hello.txt", "owner1", bytes.NewReader(want))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.HasPrefix(uri, "file://") {
		t.Fatalf("expected file:// URI, got %q", uri)
	}

	rc, err := s.Load(ctx, uri)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip mismatch: want %q got %q", want, got)
	}

	if err := s.Delete(ctx, uri); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// After delete, Load should fail.
	if _, err := s.Load(ctx, uri); err == nil {
		t.Fatal("expected error loading deleted attachment")
	}
}

func TestLoadMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Load(ctx, "file:///nonexistent/path/to/file")
	if err == nil {
		t.Fatal("expected error loading missing attachment")
	}
}

func TestLoadUnsupportedScheme(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Load(ctx, "s3://bucket/key"); err == nil {
		t.Fatal("expected error for unsupported URI scheme")
	}
}

func TestDeleteIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	uri, err := s.Upload(ctx, "foo.txt", "owner1", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if err := s.Delete(ctx, uri); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := s.Delete(ctx, uri); err != nil {
		t.Fatalf("second Delete should be idempotent, got %v", err)
	}
}

func TestHandlerNotNil(t *testing.T) {
	s := newTestStore(t)
	h := s.Handler()
	if h == nil {
		t.Fatal("Handler returned nil")
	}
	// Basic smoke: ensure the returned value satisfies http.Handler.
	var _ http.Handler = h
}
