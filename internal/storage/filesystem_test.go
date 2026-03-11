package storage

import (
	"os"
	"path/filepath"
	"testing"
)

const testID = "abcdef1234567890"

func TestWriteAndReadDocument(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	content := []byte("# Hello World\nSome markdown content.")
	if err := fs.WriteDocument(testID, content); err != nil {
		t.Fatalf("WriteDocument: %v", err)
	}

	// Verify file exists at the sharded path
	expected := filepath.Join(base, testID[0:2], testID[2:4], testID+".md")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("expected file at %s, got error: %v", expected, err)
	}

	got, err := fs.ReadDocument(testID)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("ReadDocument = %q, want %q", got, content)
	}
}

func TestWriteAndReadChunkText(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	content := []byte("chunk text content")
	if err := fs.WriteChunkText(testID, 0, content); err != nil {
		t.Fatalf("WriteChunkText: %v", err)
	}

	// Verify file naming
	expected := filepath.Join(base, testID[0:2], testID[2:4], testID+".000t.md")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("expected file at %s, got error: %v", expected, err)
	}

	got, err := fs.ReadChunkText(testID, 0)
	if err != nil {
		t.Fatalf("ReadChunkText: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("ReadChunkText = %q, want %q", got, content)
	}
}

func TestWriteAndReadChunkContext(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	content := []byte("chunk context content")
	if err := fs.WriteChunkContext(testID, 0, content); err != nil {
		t.Fatalf("WriteChunkContext: %v", err)
	}

	// Verify file naming
	expected := filepath.Join(base, testID[0:2], testID[2:4], testID+".000c.md")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("expected file at %s, got error: %v", expected, err)
	}

	got, err := fs.ReadChunkContext(testID, 0)
	if err != nil {
		t.Fatalf("ReadChunkContext: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("ReadChunkContext = %q, want %q", got, content)
	}
}

func TestChunkIndexFormatting(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	tests := []struct {
		index    int
		wantText string
		wantCtx  string
	}{
		{0, testID + ".000t.md", testID + ".000c.md"},
		{12, testID + ".012t.md", testID + ".012c.md"},
		{999, testID + ".999t.md", testID + ".999c.md"},
	}

	dir := filepath.Join(base, testID[0:2], testID[2:4])

	for _, tt := range tests {
		content := []byte("test")

		if err := fs.WriteChunkText(testID, tt.index, content); err != nil {
			t.Fatalf("WriteChunkText(index=%d): %v", tt.index, err)
		}
		if _, err := os.Stat(filepath.Join(dir, tt.wantText)); err != nil {
			t.Errorf("index %d: expected text file %s, got error: %v", tt.index, tt.wantText, err)
		}

		if err := fs.WriteChunkContext(testID, tt.index, content); err != nil {
			t.Fatalf("WriteChunkContext(index=%d): %v", tt.index, err)
		}
		if _, err := os.Stat(filepath.Join(dir, tt.wantCtx)); err != nil {
			t.Errorf("index %d: expected context file %s, got error: %v", tt.index, tt.wantCtx, err)
		}
	}
}

func TestDeleteDocumentFiles(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	// Create document and chunks
	_ = fs.WriteDocument(testID, []byte("doc"))
	_ = fs.WriteChunkText(testID, 0, []byte("t0"))
	_ = fs.WriteChunkContext(testID, 0, []byte("c0"))
	_ = fs.WriteChunkText(testID, 1, []byte("t1"))
	_ = fs.WriteChunkContext(testID, 1, []byte("c1"))

	if err := fs.DeleteDocumentFiles(testID); err != nil {
		t.Fatalf("DeleteDocumentFiles: %v", err)
	}

	dir := filepath.Join(base, testID[0:2], testID[2:4])
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir: %v", err)
	}

	// Filter for files belonging to this document
	for _, e := range entries {
		t.Errorf("file still exists after delete: %s", e.Name())
	}
}

func TestReadNonExistentDocument(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	_, err := fs.ReadDocument("nonexistent0000id")
	if err == nil {
		t.Fatal("expected error reading non-existent document, got nil")
	}
}

func TestReadNonExistentChunk(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	_, err := fs.ReadChunkText("nonexistent0000id", 0)
	if err == nil {
		t.Fatal("expected error reading non-existent chunk text, got nil")
	}

	_, err = fs.ReadChunkContext("nonexistent0000id", 0)
	if err == nil {
		t.Fatal("expected error reading non-existent chunk context, got nil")
	}
}

func TestDocDir(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	got := fs.docDir(testID)
	want := filepath.Join(base, "ab", "cd")
	if got != want {
		t.Errorf("docDir(%q) = %q, want %q", testID, got, want)
	}
}
