// Package cached provides a file-based caching wrapper for attachment stores.
package cached

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rbaliyan/mailbox/store"
)

// Store wraps an AttachmentFileStore with local file caching.
type Store struct {
	backend  store.AttachmentFileStore
	cacheDir string
	maxSize  int64 // Maximum cache size in bytes
	ttl      time.Duration
	logger   *slog.Logger

	mu        sync.RWMutex
	cacheSize int64
}

// Ensure Store implements AttachmentFileStore.
var _ store.AttachmentFileStore = (*Store)(nil)

// New creates a new cached attachment store wrapping the given backend.
func New(backend store.AttachmentFileStore, opts ...Option) (*Store, error) {
	o := &options{
		cacheDir: os.TempDir(),
		maxSize:  1 << 30, // 1GB default
		ttl:      24 * time.Hour,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(o)
	}

	// Create cache directory
	cacheDir := filepath.Join(o.cacheDir, "mailbox-attachments")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}

	s := &Store{
		backend:  backend,
		cacheDir: cacheDir,
		maxSize:  o.maxSize,
		ttl:      o.ttl,
		logger:   o.logger,
	}

	// Calculate initial cache size
	s.calculateCacheSize()

	// Start background cleanup if TTL is set
	if o.ttl > 0 {
		go s.cleanupLoop()
	}

	return s, nil
}

// Upload uploads content to the backend (caching happens on Load).
func (s *Store) Upload(ctx context.Context, filename, contentType string, content io.Reader) (string, error) {
	return s.backend.Upload(ctx, filename, contentType, content)
}

// Load returns a reader for the attachment content, using cache when available.
func (s *Store) Load(ctx context.Context, uri string) (io.ReadCloser, error) {
	cacheKey := s.cacheKey(uri)
	cachePath := filepath.Join(s.cacheDir, cacheKey)

	// Check if cached file exists and is not expired
	if info, err := os.Stat(cachePath); err == nil {
		if time.Since(info.ModTime()) < s.ttl {
			// Cache hit
			f, err := os.Open(cachePath)
			if err == nil {
				s.logger.Debug("cache hit", "uri", uri)
				// Update access time
				now := time.Now()
				_ = os.Chtimes(cachePath, now, now)
				return f, nil
			}
		} else {
			// Expired, remove it
			os.Remove(cachePath)
			s.updateCacheSize(-info.Size())
		}
	}

	// Cache miss - load from backend
	s.logger.Debug("cache miss", "uri", uri)
	reader, err := s.backend.Load(ctx, uri)
	if err != nil {
		return nil, err
	}

	// Write to cache file while reading
	return s.cacheAndRead(reader, cachePath)
}

// Delete removes the attachment from the backend and cache.
func (s *Store) Delete(ctx context.Context, uri string) error {
	// Delete from cache first
	cacheKey := s.cacheKey(uri)
	cachePath := filepath.Join(s.cacheDir, cacheKey)
	if info, err := os.Stat(cachePath); err == nil {
		os.Remove(cachePath)
		s.updateCacheSize(-info.Size())
	}

	// Delete from backend
	return s.backend.Delete(ctx, uri)
}

// ClearCache removes all cached files.
func (s *Store) ClearCache() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return fmt.Errorf("read cache dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			os.Remove(filepath.Join(s.cacheDir, entry.Name()))
		}
	}

	s.cacheSize = 0
	s.logger.Info("cache cleared")
	return nil
}

// cacheKey generates a cache key from a URI.
func (s *Store) cacheKey(uri string) string {
	h := sha256.Sum256([]byte(uri))
	return hex.EncodeToString(h[:])
}

// cacheAndRead creates a tee reader that writes to cache while reading.
func (s *Store) cacheAndRead(source io.ReadCloser, cachePath string) (io.ReadCloser, error) {
	// Create temporary file first
	tmpFile, err := os.CreateTemp(s.cacheDir, "tmp-*")
	if err != nil {
		// If we can't cache, just return the source
		s.logger.Warn("failed to create temp file for caching", "error", err)
		return source, nil
	}

	return &cachingReader{
		source:    source,
		tmpFile:   tmpFile,
		cachePath: cachePath,
		store:     s,
	}, nil
}

// cachingReader reads from source while writing to cache.
type cachingReader struct {
	source    io.ReadCloser
	tmpFile   *os.File
	cachePath string
	store     *Store
	size      int64
	closed    bool
}

func (r *cachingReader) Read(p []byte) (n int, err error) {
	n, err = r.source.Read(p)
	if n > 0 {
		// Write to temp file
		if _, writeErr := r.tmpFile.Write(p[:n]); writeErr != nil {
			r.store.logger.Warn("failed to write to cache", "error", writeErr)
		}
		r.size += int64(n)
	}
	return n, err
}

func (r *cachingReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true

	sourceErr := r.source.Close()

	// Close temp file
	if err := r.tmpFile.Close(); err != nil {
		os.Remove(r.tmpFile.Name())
		return sourceErr
	}

	// Check if we have space in cache
	if r.store.hasSpace(r.size) {
		// Rename temp file to cache path
		if err := os.Rename(r.tmpFile.Name(), r.cachePath); err != nil {
			os.Remove(r.tmpFile.Name())
			r.store.logger.Warn("failed to move temp file to cache", "error", err)
		} else {
			r.store.updateCacheSize(r.size)
			r.store.logger.Debug("cached attachment", "path", r.cachePath, "size", r.size)
		}
	} else {
		// No space, remove temp file
		os.Remove(r.tmpFile.Name())
		r.store.logger.Debug("cache full, not caching", "size", r.size)
	}

	return sourceErr
}

// hasSpace checks if there's space for a file of the given size.
func (s *Store) hasSpace(size int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cacheSize+size <= s.maxSize
}

// updateCacheSize atomically updates the cache size.
func (s *Store) updateCacheSize(delta int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheSize += delta
	if s.cacheSize < 0 {
		s.cacheSize = 0
	}
}

// calculateCacheSize calculates the current cache size.
func (s *Store) calculateCacheSize() {
	s.mu.Lock()
	defer s.mu.Unlock()

	var size int64
	if err := filepath.Walk(s.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	}); err != nil {
		s.logger.Warn("failed to calculate cache size", "error", err)
	}
	s.cacheSize = size
}

// cleanupLoop periodically removes expired cache entries.
func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(s.ttl / 2)
	defer ticker.Stop()

	for range ticker.C {
		s.cleanupExpired()
	}
}

// cleanupExpired removes expired cache entries.
func (s *Store) cleanupExpired() {
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		s.logger.Warn("failed to read cache dir for cleanup", "error", err)
		return
	}

	now := time.Now()
	var removed int
	var freedBytes int64

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(s.cacheDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if now.Sub(info.ModTime()) > s.ttl {
			if err := os.Remove(path); err == nil {
				removed++
				freedBytes += info.Size()
			}
		}
	}

	if removed > 0 {
		s.updateCacheSize(-freedBytes)
		s.logger.Info("cache cleanup completed", "removed", removed, "freed_bytes", freedBytes)
	}
}
