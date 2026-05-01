package wiki

import (
	"regexp"
	"strings"
)

// Wikilink represents a parsed [[target]] or [[target|label]] reference.
type Wikilink struct {
	Target string
	Label  string
}

// linkRe matches [[Target]] or [[Target|Label]]. Backslash-escaped brackets
// are excluded by the leading negative lookbehind alternative.
var linkRe = regexp.MustCompile(`\[\[([^\[\]\\|]+)(?:\|([^\[\]]+))?\]\]`)

// ParseWikilinks extracts wikilinks from markdown source while ignoring text
// inside fenced code blocks (```), inline code spans (`...`), and any link
// preceded by a backslash.
func ParseWikilinks(src string) []Wikilink {
	cleaned := stripCodeAndEscapes(src)
	var out []Wikilink
	for _, m := range linkRe.FindAllStringSubmatch(cleaned, -1) {
		out = append(out, Wikilink{Target: strings.TrimSpace(m[1]), Label: strings.TrimSpace(m[2])})
	}
	return out
}

// stripCodeAndEscapes blanks out fenced code blocks, inline code spans, and
// converts \[ / \] to spaces so they don't match the link regex.
func stripCodeAndEscapes(src string) string {
	var sb strings.Builder
	inFence := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "```") {
			inFence = !inFence
			sb.WriteString("\n")
			continue
		}
		if inFence {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString(stripInlineAndEscapes(line))
		sb.WriteString("\n")
	}
	return sb.String()
}

func stripInlineAndEscapes(line string) string {
	// Replace `...` spans with spaces.
	var sb strings.Builder
	inCode := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			// Drop the escape and the next char (so \[ never participates in [[).
			sb.WriteByte(' ')
			sb.WriteByte(' ')
			i++
			continue
		}
		if c == '`' {
			inCode = !inCode
			sb.WriteByte(' ')
			continue
		}
		if inCode {
			sb.WriteByte(' ')
			continue
		}
		sb.WriteByte(c)
	}
	return sb.String()
}
