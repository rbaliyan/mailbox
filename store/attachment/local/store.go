package local

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rbaliyan/mailbox/store"
)

// Store implements store.AttachmentFileStore using the local filesystem.
type Store struct {
	opts *options
}

// Ensure Store implements AttachmentFileStore.
var _ store.AttachmentFileStore = (*Store)(nil)

// New creates a new local filesystem attachment store.
// The base directory is created if it does not exist.
func New(opts ...Option) (*Store, error) {
	o := &options{
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.baseDir == "" {
		return nil, fmt.Errorf("base directory is required")
	}

	abs, err := filepath.Abs(o.baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve base dir: %w", err)
	}
	o.baseDir = abs

	if err := os.MkdirAll(o.baseDir, 0o750); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}

	return &Store{opts: o}, nil
}

// Upload writes content to the local filesystem and returns a URI.
// Returns a "file://" URI when no BaseURL is configured, or an HTTP URI otherwise.
func (s *Store) Upload(_ context.Context, filename, _ string, content io.Reader) (string, error) {
	rel := s.generateKey(filename)
	abs := filepath.Join(s.opts.baseDir, rel)

	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", fmt.Errorf("create attachment dir: %w", err)
	}

	f, err := os.Create(abs) // #nosec G304 — path is server-generated (baseDir + uuid + filepath.Base(filename))
	if err != nil {
		return "", fmt.Errorf("create attachment file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, content); err != nil {
		return "", fmt.Errorf("write attachment: %w", err)
	}

	s.opts.logger.Debug("uploaded attachment to local fs", "path", abs)

	if s.opts.baseURL != "" {
		base := strings.TrimRight(s.opts.baseURL, "/")
		return base + "/" + filepath.ToSlash(rel), nil
	}
	return "file://" + abs, nil
}

// Load opens the attachment from the local filesystem.
// Only "file://" URIs are supported; HTTP URIs returned when BaseURL is set
// cannot be loaded directly — use the HTTP handler instead.
func (s *Store) Load(_ context.Context, uri string) (io.ReadCloser, error) {
	path, err := parseFileURI(uri)
	if err != nil {
		return nil, err
	}
	if err := s.containsPath(path); err != nil {
		return nil, err
	}

	f, err := os.Open(path) // #nosec G304 — path validated by containsPath above
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("attachment not found: %s", uri)
		}
		return nil, fmt.Errorf("open attachment: %w", err)
	}
	return f, nil
}

// Delete removes the attachment file from the local filesystem.
func (s *Store) Delete(_ context.Context, uri string) error {
	path, err := parseFileURI(uri)
	if err != nil {
		return err
	}
	if err := s.containsPath(path); err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete attachment: %w", err)
	}

	s.opts.logger.Debug("deleted attachment from local fs", "path", path)

	// Remove parent directory if empty (best-effort).
	dir := filepath.Dir(path)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}

	return nil
}

// Handler returns an http.Handler that serves attachments from the base directory.
// Directory listings are disabled — only direct file paths return content.
// Mount it at the path corresponding to BaseURL in your HTTP server.
//
//	mux.Handle("/attachments/", http.StripPrefix("/attachments/", store.Handler()))
func (s *Store) Handler() http.Handler {
	return http.FileServer(noDirListFS{http.Dir(s.opts.baseDir)})
}

// noDirListFS wraps http.FileSystem to disable directory listing.
// Requests for directories return os.ErrNotExist (404) instead of an index.
type noDirListFS struct{ root http.FileSystem }

func (f noDirListFS) Open(name string) (http.File, error) {
	file, err := f.root.Open(name)
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if stat.IsDir() {
		_ = file.Close()
		return nil, os.ErrNotExist
	}
	return file, nil
}

// containsPath checks that p lies within the configured base directory,
// preventing path-traversal attacks via crafted file:// URIs.
func (s *Store) containsPath(p string) error {
	clean := filepath.Clean(p)
	if !strings.HasPrefix(clean, s.opts.baseDir+string(filepath.Separator)) {
		return fmt.Errorf("path escapes base directory: %s", p)
	}
	return nil
}

// generateKey creates a unique relative path for a new attachment.
func (s *Store) generateKey(filename string) string {
	now := time.Now().UTC()
	id := uuid.New().String()
	return filepath.Join(now.Format("2006/01/02"), id, filepath.Base(filename))
}

// parseFileURI extracts the filesystem path from a file:// URI.
func parseFileURI(uri string) (string, error) {
	if !strings.HasPrefix(uri, "file://") {
		return "", fmt.Errorf("unsupported uri scheme (expected file://): %s", uri)
	}
	return uri[7:], nil
}
