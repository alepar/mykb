package pipeline

import "strings"

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

// ChunkMarkdown splits markdown content into chunks, aware of structure.
//
// Algorithm:
//  1. Split by "\n## " (keeping the header with its section)
//  2. Each section that fits within MaxTokens is one chunk
//  3. Oversized sections are subdivided by paragraphs (double newlines),
//     greedily accumulated up to TargetTokens
//  4. Fenced code blocks (``` ... ```) are never split
func ChunkMarkdown(content string, opts ChunkOptions) []string {
	if content == "" {
		return nil
	}
	opts = opts.withDefaults()

	sections := splitBySections(content)
	var chunks []string
	for _, sec := range sections {
		sec = strings.TrimRight(sec, "\n")
		if sec == "" {
			continue
		}
		if estimateTokens(sec) <= opts.MaxTokens {
			chunks = append(chunks, sec)
		} else {
			chunks = append(chunks, splitSectionByParagraphs(sec, opts)...)
		}
	}
	return chunks
}

// splitBySections splits content on "\n## " boundaries.
// Content before the first "## " is its own section.
// Each section retains its "## " prefix.
func splitBySections(content string) []string {
	var sections []string
	parts := strings.Split(content, "\n## ")
	for i, part := range parts {
		if i == 0 {
			// First element is content before the first "\n## " split point.
			// If the original content starts with "## ", this part already
			// contains the "## " prefix, so use it as-is.
			sections = append(sections, part)
		} else {
			sections = append(sections, "## "+part)
		}
	}
	return sections
}

// splitSectionByParagraphs breaks an oversized section into chunks by
// splitting on double newlines, respecting code fences.
func splitSectionByParagraphs(section string, opts ChunkOptions) []string {
	paragraphs := splitParagraphsRespectingCodeBlocks(section)

	var chunks []string
	var current strings.Builder

	for _, para := range paragraphs {
		if current.Len() == 0 {
			// Start a new chunk with this paragraph regardless of size.
			current.WriteString(para)
			continue
		}

		combinedTokens := (current.Len() + 2 + len(para)) / 4 // +2 for "\n\n"

		if combinedTokens <= opts.TargetTokens {
			current.WriteString("\n\n")
			current.WriteString(para)
		} else {
			// Flush current chunk and start new one.
			chunks = append(chunks, current.String())
			current.Reset()
			current.WriteString(para)
		}
	}

	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

// splitParagraphsRespectingCodeBlocks splits text on double newlines but
// keeps fenced code blocks (``` ... ```) as single units.
func splitParagraphsRespectingCodeBlocks(text string) []string {
	lines := strings.Split(text, "\n")
	var paragraphs []string
	var current strings.Builder
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
		}

		if !inCodeBlock && line == "" {
			// Potential paragraph boundary — but only if we have accumulated content
			// and the next non-empty check shows it's a real double newline
			if current.Len() > 0 {
				// Look ahead: this is a paragraph break (empty line)
				// But we want to handle multiple blank lines as one break
				// Just flush the current paragraph
				paragraphs = append(paragraphs, current.String())
				current.Reset()
			}
			continue
		}

		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		paragraphs = append(paragraphs, current.String())
	}
	return paragraphs
}
