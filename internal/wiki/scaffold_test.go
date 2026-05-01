package wiki

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// expectedScaffoldFiles is the minimum set of vault-relative paths that
// ScaffoldVault must write into a fresh vault.
var expectedScaffoldFiles = []string{
	"mykb-wiki.toml",
	"CLAUDE.md",
	"Log.md",
	"entities/.gitkeep",
	"concepts/.gitkeep",
	"synthesis/.gitkeep",
	".templates/entity.md",
	".templates/concept.md",
	".templates/synthesis.md",
	".claude/skills/wiki-research/SKILL.md",
	".claude/skills/wiki-research/playbook.md",
	".claude/skills/wiki-research/examples.md",
}

func TestScaffoldVault_Fresh(t *testing.T) {
	dir := t.TempDir()
	result, err := ScaffoldVault(dir, "testkb")
	if err != nil {
		t.Fatalf("ScaffoldVault: %v", err)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("Skipped should be empty on fresh scaffold; got %d: %v", len(result.Skipped), result.Skipped)
	}
	for _, want := range expectedScaffoldFiles {
		if !slices.Contains(result.Written, want) {
			t.Errorf("expected %q in Written list; not found", want)
		}
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected file %q to exist on disk: %v", want, err)
		}
	}
}

func TestScaffoldVault_RerunIsNoop(t *testing.T) {
	dir := t.TempDir()
	if _, err := ScaffoldVault(dir, "testkb"); err != nil {
		t.Fatalf("first ScaffoldVault: %v", err)
	}

	// Snapshot file contents.
	before := snapshotVault(t, dir)

	result, err := ScaffoldVault(dir, "testkb")
	if err != nil {
		t.Fatalf("re-run ScaffoldVault: %v", err)
	}
	if len(result.Written) != 0 {
		t.Errorf("Written should be empty on re-run; got %d: %v", len(result.Written), result.Written)
	}
	for _, want := range expectedScaffoldFiles {
		if !slices.Contains(result.Skipped, want) {
			t.Errorf("expected %q in Skipped list on re-run; not found", want)
		}
	}

	// Verify no file was modified.
	after := snapshotVault(t, dir)
	for path, beforeContent := range before {
		afterContent, ok := after[path]
		if !ok {
			t.Errorf("file %q disappeared after re-run", path)
			continue
		}
		if string(beforeContent) != string(afterContent) {
			t.Errorf("file %q content changed on re-run", path)
		}
	}
}

func TestScaffoldVault_PartialFillsMissingPreservesExisting(t *testing.T) {
	dir := t.TempDir()

	// Pre-create only mykb-wiki.toml with custom content. The rest is missing.
	customConfig := []byte("name = \"testkb\"\nstale_after_days = 7\n# user customization\n")
	if err := os.WriteFile(filepath.Join(dir, "mykb-wiki.toml"), customConfig, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	result, err := ScaffoldVault(dir, "testkb")
	if err != nil {
		t.Fatalf("ScaffoldVault: %v", err)
	}

	// mykb-wiki.toml must be skipped (already present) and preserved byte-for-byte.
	if !slices.Contains(result.Skipped, "mykb-wiki.toml") {
		t.Errorf("mykb-wiki.toml should be in Skipped; got Skipped=%v", result.Skipped)
	}
	got, err := os.ReadFile(filepath.Join(dir, "mykb-wiki.toml"))
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}
	if string(got) != string(customConfig) {
		t.Errorf("mykb-wiki.toml was overwritten; want %q got %q", customConfig, got)
	}

	// The skill files (and everything else) must have been written.
	for _, want := range expectedScaffoldFiles {
		if want == "mykb-wiki.toml" {
			continue
		}
		if !slices.Contains(result.Written, want) {
			t.Errorf("expected %q in Written list; got Written=%v", want, result.Written)
		}
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected file %q to exist on disk: %v", want, err)
		}
	}
}

func TestScaffoldVault_RejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	if _, err := ScaffoldVault(dir, ""); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := ScaffoldVault(dir, "bad name with spaces"); err == nil {
		t.Error("expected error for invalid name")
	}
}

// snapshotVault returns a map of vault-relative path -> file bytes for every
// regular file under dir. Used to check that re-running scaffold doesn't
// modify any existing file.
func snapshotVault(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotVault: %v", err)
	}
	return out
}
