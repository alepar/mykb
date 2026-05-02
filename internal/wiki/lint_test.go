package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintMissingFrontmatter(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml":    `name = "main"`,
		"concepts/no-fm.md": "# A page\nbody",
		"entities/ok.md":    "---\ntype: entity\nkind: tool\ndate_updated: 2026-04-30\n---\n# OK\n",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	if !containsError(report, "concepts/no-fm.md", "missing required frontmatter") {
		t.Errorf("expected missing-frontmatter error: %+v", report)
	}
}

func TestLintBrokenWikilink(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml":  `name = "main"`,
		"concepts/a.md":   "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# A\nSee [[missing-page]].",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	if !containsError(report, "concepts/a.md", "broken wikilink") {
		t.Errorf("expected broken-wikilink error: %+v", report)
	}
}

func TestLintOrphan(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml": `name = "main"`,
		"concepts/a.md":  "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# A\n",
		"concepts/b.md":  "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# B\nSee [[a]].",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	// b is orphaned (no inbound), a has inbound from b.
	if !containsWarn(report, "concepts/b.md", "orphan") {
		t.Errorf("expected orphan warn for b.md: %+v", report)
	}
}

func TestLintStale(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml": `name = "main"
stale_after_days = 30`,
		"concepts/a.md": "---\ntype: concept\ndate_updated: 2024-01-01\n---\n# A\n",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	if !containsWarn(report, "concepts/a.md", "stale") {
		t.Errorf("expected stale warn: %+v", report)
	}
}

func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func containsError(r LintReport, path, msg string) bool {
	for _, f := range r.Errors {
		if strings.Contains(f.Path, path) && strings.Contains(f.Message, msg) {
			return true
		}
	}
	return false
}

func containsWarn(r LintReport, path, msg string) bool {
	for _, f := range r.Warnings {
		if strings.Contains(f.Path, path) && strings.Contains(f.Message, msg) {
			return true
		}
	}
	return false
}

func TestNormalizeWikilinkKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"DIY string alignment procedure", "diy string alignment procedure"},
		{"diy-string-alignment-procedure", "diy string alignment procedure"},
		{"diy_string_alignment_procedure", "diy string alignment procedure"},
		{"Foo Bar", "foo bar"},
		{"foo-bar", "foo bar"},
		{"foo_bar", "foo bar"},
		{"Multi   Space", "multi space"},
		{"  trim-me  ", "trim me"},
		{"mixed_-_separators", "mixed separators"},
		{"", ""},
		{"already lowercase", "already lowercase"},
		{"path/keeps/slash", "path/keeps/slash"},
	}
	for _, tc := range cases {
		got := normalizeWikilinkKey(tc.in)
		if got != tc.want {
			t.Errorf("normalizeWikilinkKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
