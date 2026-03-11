package pipeline

import (
	"strings"
	"testing"
)

func TestChunkMarkdown_EmptyDocument(t *testing.T) {
	chunks := ChunkMarkdown("", ChunkOptions{})
	if len(chunks) != 0 {
		t.Errorf("expected no chunks for empty document, got %d", len(chunks))
	}
}

func TestChunkMarkdown_ShortDocument(t *testing.T) {
	content := "This is a short document with very little text."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != content {
		t.Errorf("expected chunk to equal input, got %q", chunks[0])
	}
}

func TestChunkMarkdown_SplitsAtH2Headers(t *testing.T) {
	content := "## Section One\n\nContent of section one.\n\n## Section Two\n\nContent of section two.\n\n## Section Three\n\nContent of section three."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d: %v", len(chunks), chunks)
	}
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, "## ") {
			t.Errorf("chunk %d should start with '## ', got %q", i, chunk)
		}
	}
}

func TestChunkMarkdown_ContentBeforeFirstH2(t *testing.T) {
	content := "# Title\n\nSome intro text.\n\n## Section One\n\nContent one.\n\n## Section Two\n\nContent two."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d: %v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0], "# Title") {
		t.Errorf("first chunk should start with '# Title', got %q", chunks[0])
	}
	if !strings.HasPrefix(chunks[1], "## Section One") {
		t.Errorf("second chunk should start with '## Section One', got %q", chunks[1])
	}
}

func TestChunkMarkdown_H2WithNoContent(t *testing.T) {
	content := "## Empty Section\n\n## Another Section\n\nSome content."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks, got %d: %v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0], "## Empty Section") {
		t.Errorf("first chunk should be the empty section header, got %q", chunks[0])
	}
}

func TestChunkMarkdown_NestedHeadersDoNotSplit(t *testing.T) {
	content := "## Main Section\n\n### Subsection\n\nParagraph under subsection.\n\n#### Deep header\n\nMore text."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk (nested headers don't split), got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_LargeSectionSubdividedByParagraphs(t *testing.T) {
	// Build a section that exceeds MaxTokens (1500 tokens ~ 6000 chars)
	var sb strings.Builder
	sb.WriteString("## Big Section\n\n")
	for i := 0; i < 20; i++ {
		sb.WriteString(strings.Repeat("word ", 80)) // ~400 chars = ~100 tokens per paragraph
		sb.WriteString("\n\n")
	}
	content := sb.String()
	opts := ChunkOptions{TargetTokens: 200, MaxTokens: 400}
	chunks := ChunkMarkdown(content, opts)
	if len(chunks) < 2 {
		t.Errorf("expected large section to be subdivided, got %d chunks", len(chunks))
	}
	// First chunk should still start with the header
	if !strings.HasPrefix(chunks[0], "## Big Section") {
		t.Errorf("first chunk should start with header, got %q", chunks[0][:min(50, len(chunks[0]))])
	}
}

func TestChunkMarkdown_CodeBlocksKeptIntact(t *testing.T) {
	// Create content where a code block spans what would otherwise be split
	content := "## Code Example\n\n" +
		"Some intro.\n\n" +
		"```go\nfunc main() {\n\n\tprintln(\"hello\")\n\n\tprintln(\"world\")\n}\n```\n\n" +
		"Some outro."
	opts := ChunkOptions{TargetTokens: 20, MaxTokens: 500}
	chunks := ChunkMarkdown(content, opts)
	// Verify no chunk splits inside a code block
	for i, chunk := range chunks {
		opens := strings.Count(chunk, "```")
		if opens%2 != 0 {
			t.Errorf("chunk %d has unbalanced code fences (count=%d): %q", i, opens, chunk)
		}
	}
}

func TestChunkMarkdown_ChunkSizesWithinBounds(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5; i++ {
		sb.WriteString("## Section " + string(rune('A'+i)) + "\n\n")
		sb.WriteString(strings.Repeat("text ", 100)) // ~500 chars = ~125 tokens
		sb.WriteString("\n\n")
	}
	opts := ChunkOptions{TargetTokens: 200, MaxTokens: 400}
	chunks := ChunkMarkdown(sb.String(), opts)
	for i, chunk := range chunks {
		tokens := len(chunk) / 4
		if tokens > opts.MaxTokens {
			t.Errorf("chunk %d has %d estimated tokens, exceeds max %d", i, tokens, opts.MaxTokens)
		}
	}
}

func TestChunkMarkdown_SmallSectionsNotMerged(t *testing.T) {
	// Multiple small sections should each be their own chunk, NOT merged
	content := "## A\n\nSmall.\n\n## B\n\nSmall.\n\n## C\n\nSmall."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks (small sections not merged), got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_HeadersPreservedInChunks(t *testing.T) {
	content := "## First\n\nBody one.\n\n## Second\n\nBody two."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "## First") {
		t.Errorf("chunk 0 should start with '## First', got %q", chunks[0])
	}
	if !strings.HasPrefix(chunks[1], "## Second") {
		t.Errorf("chunk 1 should start with '## Second', got %q", chunks[1])
	}
}

func TestChunkMarkdown_TopLevelHeaderOnlyTreatedAsSingleSection(t *testing.T) {
	content := "# Just a Title\n\nSome body text here."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for top-level header only doc, got %d", len(chunks))
	}
}

func TestChunkMarkdown_DefaultOptions(t *testing.T) {
	content := "Hello"
	chunks := ChunkMarkdown(content, ChunkOptions{})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk with default options, got %d", len(chunks))
	}
}
