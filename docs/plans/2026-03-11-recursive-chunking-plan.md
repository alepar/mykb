# Recursive Markdown Chunking Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the 2-level markdown chunker with a recursive splitter that walks a separator hierarchy, producing coherent chunks that never break mid-table or mid-sentence.

**Architecture:** Single recursive function `splitRecursive(text, level, opts)` that tries each separator level in order. Protected blocks (code fences, tables) are extracted before splitting and treated as atomic units. Oversized protected blocks are split at safe internal boundaries (table rows, code blank lines).

**Tech Stack:** Go 1.25, standard library only (strings, regexp)

---

### Task 1: Write tests for the recursive chunker

Tests first. The new chunker must pass all these tests, which cover the separator hierarchy, protected blocks, and edge cases.

**Files:**
- Rewrite: `internal/pipeline/chunk_test.go`

**Step 1: Write the complete test file**

Create `internal/pipeline/chunk_test.go`:

```go
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
	// Single h2 section with h3 subsections, total exceeds target
	content := "## Main\n\n### Sub A\n\nContent A is here.\n\n### Sub B\n\nContent B is here."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (h3 split), got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_SplitsAtParagraphs(t *testing.T) {
	// No headers, just paragraphs that exceed target
	content := "Paragraph one with enough text to matter.\n\nParagraph two with more text.\n\nParagraph three here."
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 50})
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (paragraph split), got %d: %v", len(chunks), chunks)
	}
}

func TestChunkMarkdown_SplitsAtSentences(t *testing.T) {
	// Single paragraph with multiple sentences, exceeds target
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
	// The table should never be split across chunks
	for i, c := range chunks {
		if strings.Contains(c, "|") {
			// If chunk has table rows, it should have the header row too
			lines := strings.Split(c, "\n")
			tableLines := 0
			for _, l := range lines {
				if strings.HasPrefix(strings.TrimSpace(l), "|") {
					tableLines++
				}
			}
			if tableLines > 0 && tableLines < 3 {
				// Allow: header + separator + at least 1 data row = 3 minimum
				// But a chunk might just have 1-2 table lines if it's a small table
				// The key invariant: no chunk should have a separator row without header
				if strings.Contains(c, "|-------") && !strings.Contains(c, "| Col A") {
					t.Errorf("chunk %d has table separator without header: %q", i, c)
				}
			}
		}
	}
}

func TestChunkMarkdown_OversizedCodeBlockSplitAtBlankLines(t *testing.T) {
	// Code block exceeding API limit (simulate with very low MaxTokens)
	var sb strings.Builder
	sb.WriteString("```python\n")
	for i := 0; i < 10; i++ {
		sb.WriteString("x = " + string(rune('a'+i)) + "\n")
		sb.WriteString("\n") // blank line between statements
	}
	sb.WriteString("```")
	content := sb.String()
	// Use tiny limits to force splitting
	chunks := ChunkMarkdown(content, ChunkOptions{TargetTokens: 10, MaxTokens: 20})
	if len(chunks) < 2 {
		t.Fatalf("expected oversized code block to be split, got %d chunks", len(chunks))
	}
	// First chunk should have the opening fence
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
	// Each chunk with table content should have the header
	for i, c := range chunks {
		if strings.Contains(c, "| val") && !strings.Contains(c, "| H1") {
			t.Errorf("chunk %d has data rows without header: %q", i, c)
		}
	}
}

func TestChunkMarkdown_SmallSectionsAccumulate(t *testing.T) {
	// Small pieces at the same separator level should accumulate up to target
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
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/pipeline/ -run TestChunkMarkdown -v`
Expected: Some tests fail (h3 splitting, sentence splitting, table protection, etc.)

**Step 3: Commit test file**

```bash
git add internal/pipeline/chunk_test.go
git commit -m "test: add recursive chunking tests"
```

---

### Task 2: Implement protected block extraction

Before we can do recursive splitting, we need to identify and extract protected blocks (code fences, tables) so they aren't split by the separator logic.

**Files:**
- Modify: `internal/pipeline/chunk.go`

**Step 1: Add protected block extraction functions**

Add to `internal/pipeline/chunk.go` (keeping existing `ChunkMarkdown` and `estimateTokens` for now):

```go
const placeholder = "\x00PROTECTED_%d\x00"

// protectedBlock represents a code fence or table that should not be split.
type protectedBlock struct {
	original string
}

// extractProtectedBlocks replaces code fences and tables with placeholders,
// returning the modified text and the extracted blocks.
func extractProtectedBlocks(text string) (string, []protectedBlock) {
	var blocks []protectedBlock
	lines := strings.Split(text, "\n")
	var result strings.Builder
	var blockBuf strings.Builder
	inCodeBlock := false
	inTable := false

	flushBlock := func() {
		if blockBuf.Len() > 0 {
			idx := len(blocks)
			blocks = append(blocks, protectedBlock{original: blockBuf.String()})
			blockBuf.Reset()
			fmt.Fprintf(&result, placeholder, idx)
		}
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Code fence handling
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				// Start of code block - flush any table first
				if inTable {
					inTable = false
					flushBlock()
				}
				inCodeBlock = true
				blockBuf.WriteString(line)
				continue
			} else {
				// End of code block
				blockBuf.WriteString("\n")
				blockBuf.WriteString(line)
				inCodeBlock = false
				flushBlock()
				continue
			}
		}

		if inCodeBlock {
			blockBuf.WriteString("\n")
			blockBuf.WriteString(line)
			continue
		}

		// Table handling: lines starting with |
		isTableLine := strings.HasPrefix(trimmed, "|")
		if isTableLine {
			if !inTable {
				inTable = true
			}
			if blockBuf.Len() > 0 {
				blockBuf.WriteString("\n")
			}
			blockBuf.WriteString(line)
			continue
		}

		// Not a table line - flush table if we were in one
		if inTable {
			inTable = false
			flushBlock()
		}

		if i > 0 || result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString(line)
	}

	// Flush any trailing block
	if inCodeBlock || inTable {
		flushBlock()
	}

	return result.String(), blocks
}

// restoreProtectedBlocks replaces placeholders back with original content.
func restoreProtectedBlocks(text string, blocks []protectedBlock) string {
	for i, b := range blocks {
		ph := fmt.Sprintf(placeholder, i)
		text = strings.Replace(text, ph, b.original, 1)
	}
	return text
}
```

Add `"fmt"` to the imports.

**Step 2: Verify build**

Run: `go build ./internal/pipeline/`

**Step 3: Commit**

```bash
git add internal/pipeline/chunk.go
git commit -m "feat: add protected block extraction for code fences and tables"
```

---

### Task 3: Implement recursive splitting and oversized block handling

Replace the current `ChunkMarkdown` with the recursive algorithm.

**Files:**
- Rewrite: `internal/pipeline/chunk.go`

**Step 1: Rewrite chunk.go**

Replace the entire file with:

```go
package pipeline

import (
	"fmt"
	"strings"
)

// ChunkOptions controls the chunking behavior.
type ChunkOptions struct {
	TargetTokens int // default 800
	MaxTokens    int // default 1500
}

func (o ChunkOptions) withDefaults() ChunkOptions {
	if o.TargetTokens <= 0 {
		o.TargetTokens = 800
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 1500
	}
	return o
}

// estimateTokens returns a rough token count: len/4.
func estimateTokens(s string) int {
	return len(s) / 4
}

// separators is the hierarchy of split points, tried in order.
var separators = []string{
	"\n# ",     // h1
	"\n## ",    // h2
	"\n### ",   // h3
	"\n#### ",  // h4
	"\n##### ", // h5
	"\n###### ", // h6
	"\n\n",     // paragraph
	"\n",       // line
	". ",       // sentence (period)
	"? ",       // sentence (question)
	"! ",       // sentence (exclamation)
}

const placeholderFmt = "\x00PB_%d\x00"

// protectedBlock is a code fence or table that should not be split.
type protectedBlock struct {
	original string
}

// ChunkMarkdown splits markdown content into chunks using recursive splitting
// down a separator hierarchy. Code fences and tables are protected from splitting.
func ChunkMarkdown(content string, opts ChunkOptions) []string {
	if content == "" {
		return nil
	}
	opts = opts.withDefaults()

	// Extract protected blocks (code fences, tables).
	text, blocks := extractProtectedBlocks(content)

	// Recursively split.
	rawChunks := splitRecursive(text, 0, opts)

	// Restore protected blocks and handle oversized ones.
	var chunks []string
	for _, chunk := range rawChunks {
		chunk = restoreProtectedBlocks(chunk, blocks)
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		// Check if chunk has an oversized protected block.
		if estimateTokens(chunk) > opts.MaxTokens {
			chunks = append(chunks, splitOversizedChunk(chunk, opts)...)
		} else {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

// splitRecursive splits text at the given separator level, accumulating small
// pieces and recursing on oversized ones.
func splitRecursive(text string, level int, opts ChunkOptions) []string {
	if estimateTokens(text) <= opts.TargetTokens {
		return []string{text}
	}

	if level >= len(separators) {
		// Deepest level reached — return as-is.
		return []string{text}
	}

	sep := separators[level]
	pieces := splitKeepingSeparator(text, sep)

	// If separator didn't split anything, try next level.
	if len(pieces) <= 1 {
		return splitRecursive(text, level+1, opts)
	}

	// Greedily accumulate pieces up to TargetTokens.
	var chunks []string
	var current strings.Builder

	for _, piece := range pieces {
		piece = strings.TrimRight(piece, "\n")
		if piece == "" {
			continue
		}

		if current.Len() == 0 {
			current.WriteString(piece)
			continue
		}

		combinedTokens := estimateTokens(current.String() + "\n\n" + piece)
		if combinedTokens <= opts.TargetTokens {
			current.WriteString("\n\n")
			current.WriteString(piece)
		} else {
			// Flush current accumulator.
			accumulated := current.String()
			if estimateTokens(accumulated) > opts.TargetTokens {
				chunks = append(chunks, splitRecursive(accumulated, level+1, opts)...)
			} else {
				chunks = append(chunks, accumulated)
			}
			current.Reset()
			current.WriteString(piece)
		}
	}

	if current.Len() > 0 {
		accumulated := current.String()
		if estimateTokens(accumulated) > opts.TargetTokens {
			chunks = append(chunks, splitRecursive(accumulated, level+1, opts)...)
		} else {
			chunks = append(chunks, accumulated)
		}
	}

	return chunks
}

// splitKeepingSeparator splits text by sep, keeping the separator attached
// to the piece that follows it (e.g., "## " stays with its section).
func splitKeepingSeparator(text string, sep string) []string {
	parts := strings.Split(text, sep)
	if len(parts) <= 1 {
		return parts
	}

	pieces := make([]string, 0, len(parts))
	for i, part := range parts {
		if i == 0 {
			if part != "" {
				pieces = append(pieces, part)
			}
		} else {
			pieces = append(pieces, sep[1:]+part) // sep[1:] drops the leading \n
		}
	}
	return pieces
}

// splitOversizedChunk handles chunks that are still too large after recursive
// splitting — typically because they contain a large protected block.
func splitOversizedChunk(chunk string, opts ChunkOptions) []string {
	lines := strings.Split(chunk, "\n")

	// Detect if this is a code block.
	if strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		return splitOversizedCodeBlock(chunk, opts)
	}

	// Detect if this is a table (majority of lines start with |).
	tableLines := 0
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "|") {
			tableLines++
		}
	}
	if tableLines > len(lines)/2 {
		return splitOversizedTable(chunk, opts)
	}

	// Otherwise just return as-is (shouldn't happen with deep enough recursion).
	return []string{chunk}
}

// splitOversizedCodeBlock splits a large code block at internal blank lines,
// duplicating the opening fence tag in each chunk.
func splitOversizedCodeBlock(block string, opts ChunkOptions) []string {
	lines := strings.Split(block, "\n")
	if len(lines) < 2 {
		return []string{block}
	}

	openingFence := lines[0]
	// Remove closing fence if present.
	codeLines := lines[1:]
	hasClosingFence := false
	if len(codeLines) > 0 && strings.HasPrefix(strings.TrimSpace(codeLines[len(codeLines)-1]), "```") {
		hasClosingFence = true
		codeLines = codeLines[:len(codeLines)-1]
	}

	var chunks []string
	var current strings.Builder
	current.WriteString(openingFence)

	for _, line := range codeLines {
		candidate := current.String() + "\n" + line
		if estimateTokens(candidate) > opts.TargetTokens && current.Len() > len(openingFence) {
			// Flush with closing fence.
			current.WriteString("\n```")
			chunks = append(chunks, current.String())
			current.Reset()
			current.WriteString(openingFence)
		}
		current.WriteString("\n")
		current.WriteString(line)
	}

	if current.Len() > len(openingFence) {
		if hasClosingFence {
			current.WriteString("\n```")
		}
		chunks = append(chunks, current.String())
	}

	return chunks
}

// splitOversizedTable splits a large table at row boundaries,
// duplicating the header row and separator in each chunk.
func splitOversizedTable(chunk string, opts ChunkOptions) []string {
	lines := strings.Split(chunk, "\n")

	// Find header: first two lines starting with | (header + separator).
	var headerLines []string
	var dataLines []string
	var preTable []string
	foundHeader := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !foundHeader {
			if strings.HasPrefix(trimmed, "|") {
				headerLines = append(headerLines, line)
				if len(headerLines) == 2 {
					foundHeader = true
				}
			} else {
				preTable = append(preTable, line)
			}
		} else {
			if strings.HasPrefix(trimmed, "|") {
				dataLines = append(dataLines, line)
			}
		}
	}

	header := strings.Join(headerLines, "\n")

	var chunks []string
	var current strings.Builder
	if len(preTable) > 0 {
		current.WriteString(strings.Join(preTable, "\n"))
		current.WriteString("\n")
	}
	current.WriteString(header)

	for _, row := range dataLines {
		candidate := current.String() + "\n" + row
		if estimateTokens(candidate) > opts.TargetTokens && strings.Count(current.String(), "\n") > len(headerLines) {
			chunks = append(chunks, current.String())
			current.Reset()
			current.WriteString(header)
		}
		current.WriteString("\n")
		current.WriteString(row)
	}

	if current.Len() > len(header) {
		chunks = append(chunks, current.String())
	}

	return chunks
}

// --- protected block handling ---

// extractProtectedBlocks replaces code fences and tables with placeholders.
func extractProtectedBlocks(text string) (string, []protectedBlock) {
	var blocks []protectedBlock
	lines := strings.Split(text, "\n")
	var result strings.Builder
	var blockBuf strings.Builder
	inCodeBlock := false
	inTable := false

	flushBlock := func() {
		if blockBuf.Len() > 0 {
			idx := len(blocks)
			blocks = append(blocks, protectedBlock{original: blockBuf.String()})
			blockBuf.Reset()
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			fmt.Fprintf(&result, placeholderFmt, idx)
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Code fence toggle
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				if inTable {
					inTable = false
					flushBlock()
				}
				inCodeBlock = true
				blockBuf.WriteString(line)
				continue
			} else {
				if blockBuf.Len() > 0 {
					blockBuf.WriteString("\n")
				}
				blockBuf.WriteString(line)
				inCodeBlock = false
				flushBlock()
				continue
			}
		}

		if inCodeBlock {
			if blockBuf.Len() > 0 {
				blockBuf.WriteString("\n")
			}
			blockBuf.WriteString(line)
			continue
		}

		// Table line
		if strings.HasPrefix(trimmed, "|") {
			if !inTable {
				inTable = true
			}
			if blockBuf.Len() > 0 {
				blockBuf.WriteString("\n")
			}
			blockBuf.WriteString(line)
			continue
		}

		// Regular line — flush table if active
		if inTable {
			inTable = false
			flushBlock()
		}

		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString(line)
	}

	// Flush trailing block
	if blockBuf.Len() > 0 {
		flushBlock()
	}

	return result.String(), blocks
}

// restoreProtectedBlocks replaces placeholders back with original content.
func restoreProtectedBlocks(text string, blocks []protectedBlock) string {
	for i, b := range blocks {
		ph := fmt.Sprintf(placeholderFmt, i)
		text = strings.Replace(text, ph, b.original, 1)
	}
	return text
}
```

**Step 2: Run tests**

Run: `go test ./internal/pipeline/ -run TestChunkMarkdown -v`
Expected: All tests pass.

**Step 3: Verify full build**

Run: `go build ./cmd/... ./internal/... ./gen/...`

**Step 4: Commit**

```bash
git add internal/pipeline/chunk.go internal/pipeline/chunk_test.go
git commit -m "feat: recursive markdown chunker with protected blocks"
```

---

### Task 3: Rebuild Docker and integration test

**Step 1: Rebuild and restart mykb container**

```bash
docker compose build mykb && docker compose up -d mykb
```

**Step 2: Delete existing data and re-ingest**

The existing documents were chunked with the old algorithm. Delete them and re-ingest to test with the new chunker.

```bash
# Delete all documents from postgres
docker exec mykb-postgres-1 psql -U mykb -c "DELETE FROM chunks; DELETE FROM documents;"

# Clear Qdrant
curl -s -X DELETE "http://localhost:6336/collections/mykb"

# Clear Meilisearch
curl -s -X DELETE "http://localhost:7701/indexes/mykb" -H "Authorization: Bearer $(grep MEILISEARCH_KEY .env | cut -d= -f2)"

# Restart mykb to recreate indices
docker compose restart mykb
```

**Step 3: Ingest a test document**

```bash
go build -o mykb ./cmd/mykb/ && ./mykb ingest "https://go.dev/blog/maps"
```

**Step 4: Test query**

```bash
./mykb query --top-k 3 --lines 5 "how to iterate over map keys"
```

Expected: Results with coherent chunk text that doesn't break mid-sentence or mid-table.

**Step 5: Test query with --no-merge**

```bash
./mykb query --no-merge --top-k 5 --lines 3 "map declaration and initialization"
```

Expected: Individual chunks that correspond to semantic sections of the document.
