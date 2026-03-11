# Recursive Markdown Chunking Design

## Goal

Replace the current 2-level chunker with a recursive splitter that walks down a separator hierarchy until chunks fit within TargetTokens, producing coherent chunks that never break mid-table or mid-sentence.

## Algorithm

Given text and a separator level, split at that separator. Greedily accumulate pieces into chunks up to TargetTokens. Any piece that exceeds TargetTokens on its own recurses to the next separator level. At the deepest level (sentence), if a sentence still exceeds target, include it as-is.

### Separator hierarchy (tried in order)

1. `\n# ` (h1)
2. `\n## ` (h2)
3. `\n### ` through `\n###### ` (h3-h6)
4. `\n\n` (paragraph / double newline)
5. `\n` (single newline)
6. Sentence boundary (`. `, `? `, `! `)

### Protected blocks

Never split inside:
- Fenced code blocks (`` ``` ... ``` ``)
- Tables (contiguous lines starting with `|`)

These are treated as atomic units during splitting. If a protected block exceeds TargetTokens but fits within the embedding API limit (16k tokens), it becomes its own chunk with a logged warning.

If a protected block exceeds the API limit:
- **Tables**: split at row boundaries, duplicating the header row at the top of each chunk
- **Code blocks**: split at blank line boundaries within the fence, duplicating the opening ``` tag

### Config

Same `ChunkOptions` with same defaults:
- `TargetTokens`: 800 (soft target for accumulation)
- `MaxTokens`: 1500 (warning threshold)

## Files changed

- Rewrite: `internal/pipeline/chunk.go`
- Rewrite: `internal/pipeline/chunk_test.go`

Same `ChunkMarkdown(content string, opts ChunkOptions) []string` signature. No changes to callers.
