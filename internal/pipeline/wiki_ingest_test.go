package pipeline

import (
	"context"
	"testing"
)

// TestStripFrontmatterBeforeChunking ensures the wiki ingest pipeline strips
// frontmatter before passing content to the chunker, so embeddings are clean.
func TestStripFrontmatterBeforeChunking(t *testing.T) {
	body := "---\ntype: concept\n---\n# Title\n\nBody text here."
	stripped := stripFrontmatterForChunking(body)
	if stripped != "# Title\n\nBody text here." {
		t.Errorf("got %q", stripped)
	}
}

// TestComputeContentHash ensures sha256 over the full body (frontmatter
// included) is what gets stored, so frontmatter changes trigger re-ingest.
func TestComputeContentHash(t *testing.T) {
	a := ComputeContentHash("---\nx: 1\n---\nBody")
	b := ComputeContentHash("---\nx: 2\n---\nBody")
	if a == b {
		t.Error("expected different hashes when frontmatter differs")
	}
	c := ComputeContentHash("---\nx: 1\n---\nBody")
	if a != c {
		t.Error("expected stable hash for identical input")
	}
}

// TestRunWikiIngestSkipsIfHashMatches: when the existing document's
// content_hash matches the new one, the pipeline returns a no-op result
// without re-embedding.
func TestRunWikiIngestSkipsIfHashMatches(t *testing.T) {
	t.Skip("integration test — covered by Task 8 server-level test against real stack")
	_ = context.Background()
}
