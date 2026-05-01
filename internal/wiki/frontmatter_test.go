package wiki

import (
	"reflect"
	"testing"
)

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name, in         string
		wantFM, wantBody string
	}{
		{
			"basic",
			"---\ntype: entity\n---\n# Title\n\nBody.",
			"type: entity\n",
			"# Title\n\nBody.",
		},
		{
			"no_frontmatter",
			"# Title\n\nBody.",
			"",
			"# Title\n\nBody.",
		},
		{
			"frontmatter_with_blank_lines",
			"---\ntype: concept\nrelated:\n  - a\n  - b\n---\n\n# Title\n",
			"type: concept\nrelated:\n  - a\n  - b\n",
			"\n# Title\n",
		},
		{
			"closing_marker_inside_code_block_does_not_match",
			"# Title\n\n```\n---\n```\n",
			"",
			"# Title\n\n```\n---\n```\n",
		},
		{
			"frontmatter_only",
			"---\ntype: entity\n---\n",
			"type: entity\n",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body := SplitFrontmatter(tt.in)
			if fm != tt.wantFM || body != tt.wantBody {
				t.Errorf("got fm=%q body=%q\nwant fm=%q body=%q", fm, body, tt.wantFM, tt.wantBody)
			}
		})
	}
}

func TestParseFrontmatter(t *testing.T) {
	in := "type: entity\nkind: model\naliases: [a, b]\ndate_updated: 2026-04-30\n"
	got, err := ParseFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type":         "entity",
		"kind":         "model",
		"aliases":      []any{"a", "b"},
		"date_updated": "2026-04-30",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestExtractTitle(t *testing.T) {
	tests := []struct {
		name, body, fallback, want string
	}{
		{"first_h1", "# Foo\n\nBar", "fallback", "Foo"},
		{"h1_after_blank", "\n\n# Foo\n", "fallback", "Foo"},
		{"frontmatter_then_h1", "---\ntype: x\n---\n# Foo\n", "fallback", "Foo"},
		{"no_h1_falls_back", "Just text.", "concepts/foo.md", "concepts/foo.md"},
		{"h2_does_not_count", "## Sub\n", "fallback", "fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTitle(tt.body, tt.fallback)
			if got != tt.want {
				t.Errorf("got %q want %q", got, tt.want)
			}
		})
	}
}
