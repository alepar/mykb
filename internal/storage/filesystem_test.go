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

	// Verify file naming: {uuid}.{NNN}.md
	expected := filepath.Join(base, testID[0:2], testID[2:4], testID+".000.md")
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

func TestChunkIndexFormatting(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	tests := []struct {
		index int
		want  string
	}{
		{0, testID + ".000.md"},
		{12, testID + ".012.md"},
		{999, testID + ".999.md"},
	}

	dir := filepath.Join(base, testID[0:2], testID[2:4])

	for _, tt := range tests {
		if err := fs.WriteChunkText(testID, tt.index, []byte("test")); err != nil {
			t.Fatalf("WriteChunkText(index=%d): %v", tt.index, err)
		}
		if _, err := os.Stat(filepath.Join(dir, tt.want)); err != nil {
			t.Errorf("index %d: expected file %s, got error: %v", tt.index, tt.want, err)
		}
	}
}

func TestDeleteDocumentFiles(t *testing.T) {
	base := t.TempDir()
	fs := NewFilesystemStore(base)

	_ = fs.WriteDocument(testID, []byte("doc"))
	_ = fs.WriteChunkText(testID, 0, []byte("t0"))
	_ = fs.WriteChunkText(testID, 1, []byte("t1"))

	if err := fs.DeleteDocumentFiles(testID); err != nil {
		t.Fatalf("DeleteDocumentFiles: %v", err)
	}

	dir := filepath.Join(base, testID[0:2], testID[2:4])
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir: %v", err)
	}

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
