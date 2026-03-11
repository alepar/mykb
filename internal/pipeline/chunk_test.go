package pipeline

import (
	"strings"
	"testing"
)

func TestChunkMarkdown_EmptyDocument(t *testing.T) {
	chunks := ChunkMarkdown("", ChunkOptions{})
	if len(chunks) != 0 {
		t.Errorf("expected no chunks, got %d", len(chunks))
	}
}

func TestChunkMarkdown_ShortDocument(t *testing.T) {
	content := "This is a short document."
	chunks := ChunkMarkdown(content, ChunkOptions{})
	if len(chunks) != 1 || chunks[0] != content {
		t.Errorf("expected 1 chunk with original content, got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_SplitsAtH1(t *testing.T) {
	content := "# Section One\n\nContent one.\n\n# Section Two\n\nContent two."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0], "# Section One") {
		t.Errorf("chunk 0: %q", chunks[0])
	}
	if !strings.HasPrefix(chunks[1], "# Section Two") {
		t.Errorf("chunk 1: %q", chunks[1])
	}
}

func TestChunkMarkdown_SplitsAtH2(t *testing.T) {
	content := "## A\n\nContent A.\n\n## B\n\nContent B.\n\n## C\n\nContent C."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %v", len(chunks), chunks)
	}
	for i, c := range chunks {
		if !strings.HasPrefix(c, "## ") {
			t.Errorf("chunk %d should start with '## ': %q", i, c)
		}
	}
}

func TestChunkMarkdown_SplitsAtH3WhenH2TooLarge(t *testing.T) {
	content := "## Main\n\n### Sub A\n\nContent A is here.\n\n### Sub B\n\nContent B is here."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (h3 split), got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_SplitsAtParagraphs(t *testing.T) {
	content := "Paragraph one with enough text to matter.\n\nParagraph two with more text.\n\nParagraph three here."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (paragraph split), got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_SplitsAtSentences(t *testing.T) {
	content := "First sentence here. Second sentence here. Third sentence here. Fourth sentence here."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 5, MaxTokens: 50})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (sentence split), got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_CodeBlockKeptIntact(t *testing.T) {
	content := "Some intro.\n\n```go\nfunc main() {\n\n\tprintln(\"hello\")\n\n\tprintln(\"world\")\n}\n```\n\nSome outro."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 500})
	for i, c := range chunks {
		opens := strings.Count(c, "```")
		if opens%2 != 0 {
			t.Errorf("chunk %d has unbalanced code fences: %q", i, c)
		}
	}
}

func TestChunkMarkdown_TableKeptIntact(t *testing.T) {
	content := "Some text.\n\n| Col A | Col B |\n|-------|-------|\n| val 1 | val 2 |\n| val 3 | val 4 |\n\nMore text."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 5, MaxTokens: 500})
	for i, c := range chunks {
		if strings.Contains(c, "|") {
			if strings.Contains(c, "|-------") && !strings.Contains(c, "| Col A") {
				t.Errorf("chunk %d has table separator without header: %q", i, c)
			}
		}
	}
}

func TestChunkMarkdown_OversizedCodeBlockSplitAtBlankLines(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("```python\n")
	for i := 0; i < 10; i++ {
		sb.WriteString("x = " + string(rune('a'+i)) + "\n")
		sb.WriteString("\n")
	}
	sb.WriteString("```")
	content := sb.String()
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 20})
	if len(chunks) < 2 {
		t.Fatalf("expected oversized code block to be split, got %d chunks", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "```python") {
		t.Errorf("first chunk should start with opening fence: %q", chunks[0])
	}
}

func TestChunkMarkdown_OversizedTableSplitAtRows(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("| H1 | H2 |\n|-----|-----|\n")
	for i := 0; i < 20; i++ {
		sb.WriteString("| val | val |\n")
	}
	content := sb.String()
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 20})
	if len(chunks) < 2 {
		t.Fatalf("expected oversized table to be split, got %d chunks", len(chunks))
	}
	for i, c := range chunks {
		if strings.Contains(c, "| val") && !strings.Contains(c, "| H1") {
			t.Errorf("chunk %d has data rows without header: %q", i, c)
		}
	}
}

func TestChunkMarkdown_SmallSectionsAccumulate(t *testing.T) {
	content := "A.\n\nB.\n\nC.\n\nD."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 800, MaxTokens: 1500})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk (small paragraphs accumulated), got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_HeaderPreservedInChunks(t *testing.T) {
	content := "## First\n\nBody one.\n\n## Second\n\nBody two."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "## First") {
		t.Errorf("chunk 0: %q", chunks[0])
	}
	if !strings.HasPrefix(chunks[1], "## Second") {
		t.Errorf("chunk 1: %q", chunks[1])
	}
}

func TestChunkMarkdown_ContentBeforeFirstHeader(t *testing.T) {
	content := "Intro text.\n\n## Section\n\nContent."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d: %v", len(chunks), chunks)
	}
	if !strings.Contains(chunks[0], "Intro text") {
		t.Errorf("first chunk should contain intro: %q", chunks[0])
	}
}

func TestChunkMarkdown_DefaultOptions(t *testing.T) {
	chunks := ChunkMarkdown("Hello", ChunkOptions{})
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestChunkMarkdown_NoEmptyChunks(t *testing.T) {
	content := "## A\n\n\n\n## B\n\nContent.\n\n\n\n## C\n\n"
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	for i, c := range chunks {
		if strings.TrimSpace(c) == "" {
			t.Errorf("chunk %d is empty", i)
		}
	}
}
