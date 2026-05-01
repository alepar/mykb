package wiki

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SplitFrontmatter separates leading YAML frontmatter (between --- markers)
// from the markdown body. If no frontmatter is present, returns ("", input).
// Note: the closing --- must be at the start of a line and must occur before
// any code fence opens. The implementation only inspects the very start of
// the document, so a stray "---" later (e.g. inside a code block) is ignored.
func SplitFrontmatter(s string) (frontmatter, body string) {
	if !strings.HasPrefix(s, "---\n") {
		return "", s
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		// Handle trailing case where file ends with "---\n" with no newline after.
		if strings.HasSuffix(rest, "\n---") {
			return rest[:len(rest)-3], ""
		}
		return "", s
	}
	return rest[:end+1], rest[end+5:]
}

// ParseFrontmatter parses YAML frontmatter into a map.
// Scalar values that yaml.v3 would normally decode as time.Time (e.g. dates)
// are kept as plain strings to simplify downstream handling.
func ParseFrontmatter(fm string) (map[string]any, error) {
	if fm == "" {
		return map[string]any{}, nil
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(fm), &node); err != nil {
		return nil, err
	}
	if node.Kind == 0 {
		return map[string]any{}, nil
	}
	// Unwrap the document node.
	doc := &node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	result, err := nodeToAny(doc)
	if err != nil {
		return nil, err
	}
	m, ok := result.(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return m, nil
}

// nodeToAny converts a yaml.Node to a plain Go value, treating all scalars
// as strings (which prevents yaml.v3 from auto-converting dates to time.Time).
func nodeToAny(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value, nil
	case yaml.SequenceNode:
		seq := make([]any, len(n.Content))
		for i, child := range n.Content {
			v, err := nodeToAny(child)
			if err != nil {
				return nil, err
			}
			seq[i] = v
		}
		return seq, nil
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, err := nodeToAny(n.Content[i])
			if err != nil {
				return nil, err
			}
			val, err := nodeToAny(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[fmt.Sprintf("%v", key)] = val
		}
		return m, nil
	default:
		return nil, nil
	}
}

// ExtractTitle returns the first H1 heading (lines starting with "# "),
// scanning past any leading frontmatter. Falls back to the provided string
// if no H1 is found.
func ExtractTitle(body, fallback string) string {
	_, body = stripLeadingFrontmatter(body)
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(t, "#"))
		}
		if t != "" && !strings.HasPrefix(t, "#") {
			// First non-heading content; no H1 will follow.
			break
		}
	}
	return fallback
}

func stripLeadingFrontmatter(s string) (fm, body string) {
	return SplitFrontmatter(s)
}
