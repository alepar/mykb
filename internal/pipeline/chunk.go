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
	"\n# ",
	"\n## ",
	"\n### ",
	"\n#### ",
	"\n##### ",
	"\n###### ",
	"\n\n",
	"\n",
	". ",
	"? ",
	"! ",
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

	text, blocks := extractProtectedBlocks(content)
	rawChunks := splitRecursive(text, 0, opts)

	var chunks []string
	for _, chunk := range rawChunks {
		chunk = restoreProtectedBlocks(chunk, blocks)
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if estimateTokens(chunk) >= opts.MaxTokens {
			chunks = append(chunks, splitOversizedChunk(chunk, opts)...)
		} else {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

// isHeaderSeparator returns true if the separator level corresponds to a
// markdown heading (h1-h6). Header-level splits do not accumulate small pieces.
func isHeaderSeparator(level int) bool {
	return level < 6
}

// splitRecursive splits text at the given separator level, accumulating small
// pieces and recursing on oversized ones.
func splitRecursive(text string, level int, opts ChunkOptions) []string {
	if level >= len(separators) {
		return []string{text}
	}

	sep := separators[level]
	pieces := splitKeepingSeparator(text, sep)

	// For header-level separators (h1-h6), always split when the separator is
	// found, regardless of size. Each piece stands on its own.
	if isHeaderSeparator(level) {
		if len(pieces) <= 1 {
			return splitRecursive(text, level+1, opts)
		}
		var chunks []string
		for _, piece := range pieces {
			piece = strings.TrimRight(piece, "\n")
			if piece == "" {
				continue
			}
			if estimateTokens(piece) > opts.TargetTokens {
				chunks = append(chunks, splitRecursive(piece, level+1, opts)...)
			} else {
				chunks = append(chunks, piece)
			}
		}
		return chunks
	}

	// For non-header separators, only split if the text exceeds the target.
	if estimateTokens(text) <= opts.TargetTokens {
		return []string{text}
	}

	if len(pieces) <= 1 {
		return splitRecursive(text, level+1, opts)
	}

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
// to the piece that follows it.
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
			// For separators starting with \n, drop the leading \n
			// so the header/marker stays with the content.
			prefix := sep
			if strings.HasPrefix(sep, "\n") {
				prefix = sep[1:]
			}
			pieces = append(pieces, prefix+part)
		}
	}
	return pieces
}

// splitOversizedChunk handles chunks still too large after recursive splitting.
func splitOversizedChunk(chunk string, opts ChunkOptions) []string {
	lines := strings.Split(chunk, "\n")

	if strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		return splitOversizedCodeBlock(chunk, opts)
	}

	tableLines := 0
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "|") {
			tableLines++
		}
	}
	if tableLines > len(lines)/2 {
		return splitOversizedTable(chunk, opts)
	}

	return []string{chunk}
}

// splitOversizedCodeBlock splits a large code block, duplicating the opening fence.
func splitOversizedCodeBlock(block string, opts ChunkOptions) []string {
	lines := strings.Split(block, "\n")
	if len(lines) < 2 {
		return []string{block}
	}

	openingFence := lines[0]
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

// splitOversizedTable splits a large table at row boundaries, duplicating the header.
func splitOversizedTable(chunk string, opts ChunkOptions) []string {
	lines := strings.Split(chunk, "\n")

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

		if inTable {
			inTable = false
			flushBlock()
		}

		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString(line)
	}

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
