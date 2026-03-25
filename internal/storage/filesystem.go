package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FilesystemStore reads/writes documents and chunks using a two-level
// directory sharding scheme based on the first four characters of the ID.
type FilesystemStore struct {
	baseDir string
}

// NewFilesystemStore creates a new FilesystemStore rooted at baseDir.
func NewFilesystemStore(baseDir string) *FilesystemStore {
	return &FilesystemStore{baseDir: baseDir}
}

// docDir returns "{baseDir}/{id[0:2]}/{id[2:4]}".
func (fs *FilesystemStore) docDir(id string) string {
	return filepath.Join(fs.baseDir, id[0:2], id[2:4])
}

// WriteDocument writes the full document markdown to disk.
func (fs *FilesystemStore) WriteDocument(id string, content []byte) error {
	dir := fs.docDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create doc dir: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, id+".md"), content, 0o644)
}

// WriteDocumentRaw writes the unfiltered raw markdown to disk for debugging.
func (fs *FilesystemStore) WriteDocumentRaw(id string, content []byte) error {
	dir := fs.docDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create doc dir: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, id+".raw.md"), content, 0o644)
}

// ReadDocument reads the full document markdown from disk.
func (fs *FilesystemStore) ReadDocument(id string) ([]byte, error) {
	return os.ReadFile(filepath.Join(fs.docDir(id), id+".md"))
}

// WriteChunkText writes a chunk's text content to disk.
// File format: {uuid}.{NNN}.md (e.g. abc123.000.md)
func (fs *FilesystemStore) WriteChunkText(id string, index int, content []byte) error {
	dir := fs.docDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create doc dir: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, chunkFileName(id, index)), content, 0o644)
}

// ReadChunkText reads a chunk's text content from disk.
func (fs *FilesystemStore) ReadChunkText(id string, index int) ([]byte, error) {
	return os.ReadFile(filepath.Join(fs.docDir(id), chunkFileName(id, index)))
}

// DeleteDocumentFiles removes all files belonging to a document (main doc + all chunks).
func (fs *FilesystemStore) DeleteDocumentFiles(id string) error {
	dir := fs.docDir(id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read doc dir: %w", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), id) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return fmt.Errorf("remove %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}

func chunkFileName(id string, index int) string {
	return fmt.Sprintf("%s.%03d.md", id, index)
}

// WritePrefetchHTML writes pre-fetched HTML content for a document.
// Used when the browser extension sends rendered HTML alongside the URL.
func (fs *FilesystemStore) WritePrefetchHTML(id string, html []byte) error {
	dir := fs.docDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create doc dir: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, id+".prefetch.html"), html, 0o644)
}

// ReadPrefetchHTML reads pre-fetched HTML for a document.
func (fs *FilesystemStore) ReadPrefetchHTML(id string) ([]byte, error) {
	return os.ReadFile(filepath.Join(fs.docDir(id), id+".prefetch.html"))
}

// HasPrefetchHTML returns true if a prefetch HTML file exists for a document.
func (fs *FilesystemStore) HasPrefetchHTML(id string) bool {
	_, err := os.Stat(filepath.Join(fs.docDir(id), id+".prefetch.html"))
	return err == nil
}

// DeletePrefetchHTML removes the prefetch HTML file (best-effort).
func (fs *FilesystemStore) DeletePrefetchHTML(id string) {
	_ = os.Remove(filepath.Join(fs.docDir(id), id+".prefetch.html"))
}
