# LLM-wiki on mykb Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend mykb so that an Obsidian-style markdown wiki (curated entity / concept / synthesis pages, checked into git) can be ingested into mykb's hybrid search alongside raw web sources, maintained by Claude Code via a new `mykb wiki` CLI noun.

**Architecture:** Wiki pages live in a separate git-versioned vault. mykb stores them as type-blind documents under synthetic URLs `wiki://<wiki-name>/<vault-relative-path>`. A new pipeline branch — keyed off the `wiki://` URL prefix — skips Crawl4AI and the filesystem cache, strips frontmatter before chunking, and otherwise reuses the existing chunk → embed → dual-index pipeline. New CLI subcommands provide vault auto-discovery and operations.

**Tech Stack:** Go (server, CLI, pipeline), PostgreSQL (metadata + new `content_hash` column), Qdrant (vectors), Meilisearch (FTS), ConnectRPC (`proto/mykb/v1/kb.proto`), TOML for vault config (`pelletier/go-toml/v2` if not already present, otherwise stdlib-friendly equivalent).

**Spec:** [`docs/superpowers/specs/2026-04-30-llm-wiki-on-mykb-design.md`](../specs/2026-04-30-llm-wiki-on-mykb-design.md)

**File map:**
- New: `internal/storage/migrations/003_content_hash.sql`
- New: `internal/wiki/url.go`, `internal/wiki/url_test.go` — URL codec
- New: `internal/wiki/frontmatter.go`, `internal/wiki/frontmatter_test.go` — frontmatter parser/stripper, title extractor
- New: `internal/wiki/config.go`, `internal/wiki/config_test.go` — vault config TOML, auto-discovery
- New: `internal/wiki/wikilinks.go`, `internal/wiki/wikilinks_test.go` — `[[...]]` parser
- New: `internal/wiki/lint.go`, `internal/wiki/lint_test.go` — lint checks
- New: `internal/pipeline/wiki_ingest.go`, `internal/pipeline/wiki_ingest_test.go` — synchronous wiki ingest
- New: `cmd/mykb/wiki.go` — `mykb wiki` subcommand dispatcher and subcommands
- Modify: `internal/storage/postgres.go` — add `ContentHash` field, `InsertDocumentWithHash`, `GetDocumentByURL`-style hash queries, `ListWikiDocuments`
- Modify: `internal/server/server.go` — `IngestMarkdown`, `ListWikiDocuments` handlers
- Modify: `proto/mykb/v1/kb.proto` — new RPCs and messages
- Modify: `cmd/mykb/main.go` — route `wiki` to dispatcher in `wiki.go`
- Modify: `internal/server/http.go` — extend `/healthz/deep` (optional final task)

---

## Task 1: Schema migration for `content_hash`

**Files:**
- Create: `internal/storage/migrations/003_content_hash.sql`
- Test: existing migration runner test (if present); otherwise verified via integration test in Task 5.

- [ ] **Step 1: Write the migration**

Create `internal/storage/migrations/003_content_hash.sql`:

```sql
-- Adds content_hash column for wiki document idempotent re-ingest.
-- NULL for raw-source documents (which use crawled_at for staleness checks).
ALTER TABLE documents ADD COLUMN content_hash TEXT;

CREATE INDEX idx_documents_content_hash ON documents(content_hash) WHERE content_hash IS NOT NULL;
```

- [ ] **Step 2: Run the migration locally**

```bash
just up
docker compose exec postgres psql -U mykb -d mykb -c "\d documents"
```

Expected: `content_hash` column appears (text, nullable). The migration runner in `internal/storage/postgres.go` (`RunMigrations`) auto-applies new files in `internal/storage/migrations/` because of the `//go:embed migrations/*.sql` directive — but we need to actually run the binary once for it to apply. Triggered by `just up` (which restarts the API service) or by running the API server.

If the column is missing, restart the mykb container: `docker compose restart mykb`, then re-check.

- [ ] **Step 3: Commit**

```bash
git add internal/storage/migrations/003_content_hash.sql
git commit -m "feat: add content_hash column for wiki document sync"
```

---

## Task 2: URL codec (vault path ↔ `wiki://` URL)

**Files:**
- Create: `internal/wiki/url.go`
- Test: `internal/wiki/url_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/wiki/url_test.go`:

```go
package wiki

import "testing"

func TestVaultPathToURL(t *testing.T) {
	tests := []struct {
		name, wikiName, vaultPath, want string
	}{
		{"basic", "main", "concepts/foo.md", "wiki://main/concepts/foo.md"},
		{"nested", "main", "synthesis/2026/q2/x.md", "wiki://main/synthesis/2026/q2/x.md"},
		{"backslashes_normalized", "main", "concepts\\foo.md", "wiki://main/concepts/foo.md"},
		{"leading_slash_stripped", "main", "/concepts/foo.md", "wiki://main/concepts/foo.md"},
		{"different_wiki", "personal", "concepts/foo.md", "wiki://personal/concepts/foo.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VaultPathToURL(tt.wikiName, tt.vaultPath)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestURLToVaultPath(t *testing.T) {
	tests := []struct {
		name, url, wantWiki, wantPath string
		wantErr                       bool
	}{
		{"basic", "wiki://main/concepts/foo.md", "main", "concepts/foo.md", false},
		{"nested", "wiki://main/synthesis/2026/q2/x.md", "main", "synthesis/2026/q2/x.md", false},
		{"non_wiki_scheme", "https://example.com/x", "", "", true},
		{"missing_path", "wiki://main", "", "", true},
		{"empty", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotWiki, gotPath, err := URLToVaultPath(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if gotWiki != tt.wantWiki || gotPath != tt.wantPath {
					t.Errorf("got (%q, %q), want (%q, %q)", gotWiki, gotPath, tt.wantWiki, tt.wantPath)
				}
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	wikiName := "main"
	vaultPaths := []string{"concepts/foo.md", "entities/bar/baz.md", "synthesis/answer.md"}
	for _, vp := range vaultPaths {
		t.Run(vp, func(t *testing.T) {
			url, err := VaultPathToURL(wikiName, vp)
			if err != nil {
				t.Fatal(err)
			}
			gotWiki, gotPath, err := URLToVaultPath(url)
			if err != nil {
				t.Fatal(err)
			}
			if gotWiki != wikiName || gotPath != vp {
				t.Errorf("round-trip mismatch: %q -> %q -> (%q,%q)", vp, url, gotWiki, gotPath)
			}
		})
	}
}

func TestIsWikiURL(t *testing.T) {
	if !IsWikiURL("wiki://main/x.md") {
		t.Error("expected true for wiki URL")
	}
	if IsWikiURL("https://example.com") {
		t.Error("expected false for https URL")
	}
}
```

- [ ] **Step 2: Run tests; verify they fail with "package wiki not found" or similar**

```bash
go test ./internal/wiki/ -run TestVaultPathToURL -v
```

Expected: build failure (file `url.go` doesn't exist).

- [ ] **Step 3: Implement**

Create `internal/wiki/url.go`:

```go
// Package wiki provides primitives for ingesting and managing a markdown
// wiki vault as type-blind documents in mykb. URLs are synthetic and follow
// the scheme wiki://<wiki-name>/<vault-relative-path>.
package wiki

import (
	"fmt"
	"strings"
)

const URLScheme = "wiki://"

// VaultPathToURL builds a wiki:// URL from a wiki name and vault-relative path.
// Backslashes are normalized to forward slashes; leading slashes are stripped.
func VaultPathToURL(wikiName, vaultPath string) (string, error) {
	if wikiName == "" {
		return "", fmt.Errorf("wiki name is empty")
	}
	if vaultPath == "" {
		return "", fmt.Errorf("vault path is empty")
	}
	p := strings.ReplaceAll(vaultPath, "\\", "/")
	p = strings.TrimLeft(p, "/")
	return URLScheme + wikiName + "/" + p, nil
}

// URLToVaultPath parses a wiki:// URL into wiki name and vault-relative path.
func URLToVaultPath(url string) (wikiName, vaultPath string, err error) {
	if !strings.HasPrefix(url, URLScheme) {
		return "", "", fmt.Errorf("not a wiki URL: %q", url)
	}
	rest := strings.TrimPrefix(url, URLScheme)
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash == len(rest)-1 {
		return "", "", fmt.Errorf("malformed wiki URL: %q", url)
	}
	return rest[:slash], rest[slash+1:], nil
}

// IsWikiURL reports whether the URL uses the wiki:// scheme.
func IsWikiURL(url string) bool {
	return strings.HasPrefix(url, URLScheme)
}
```

- [ ] **Step 4: Run tests; verify they pass**

```bash
go test ./internal/wiki/ -v
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/url.go internal/wiki/url_test.go
git commit -m "feat: add wiki URL codec for vault path translation"
```

---

## Task 3: Frontmatter parser, stripper, and title extractor

**Files:**
- Create: `internal/wiki/frontmatter.go`
- Test: `internal/wiki/frontmatter_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/wiki/frontmatter_test.go`:

```go
package wiki

import (
	"reflect"
	"testing"
)

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name, in       string
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
```

- [ ] **Step 2: Run tests; verify they fail**

```bash
go test ./internal/wiki/ -run TestSplitFrontmatter -v
```

Expected: build failure.

- [ ] **Step 3: Implement**

Create `internal/wiki/frontmatter.go`:

```go
package wiki

import (
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
func ParseFrontmatter(fm string) (map[string]any, error) {
	if fm == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(fm), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
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
```

Note: this assumes `gopkg.in/yaml.v3` is already in `go.mod` (mykb uses it elsewhere). Verify with `grep yaml.v3 go.mod` before adding to imports. If absent, add it: `go get gopkg.in/yaml.v3`.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/wiki/ -v
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/frontmatter.go internal/wiki/frontmatter_test.go go.mod go.sum
git commit -m "feat: add frontmatter parser/stripper and title extractor"
```

---

## Task 4: Vault config (TOML) and auto-discovery

**Files:**
- Create: `internal/wiki/config.go`
- Test: `internal/wiki/config_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/wiki/config_test.go`:

```go
package wiki

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseVaultConfig(t *testing.T) {
	in := `name = "main"
stale_after_days = 90
`
	cfg, err := ParseVaultConfig([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "main" {
		t.Errorf("name: got %q want main", cfg.Name)
	}
	if cfg.StaleAfterDays != 90 {
		t.Errorf("stale: got %d want 90", cfg.StaleAfterDays)
	}
}

func TestParseVaultConfigDefaults(t *testing.T) {
	in := `name = "personal"`
	cfg, err := ParseVaultConfig([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StaleAfterDays != 180 {
		t.Errorf("stale default: got %d want 180", cfg.StaleAfterDays)
	}
}

func TestParseVaultConfigRequiresName(t *testing.T) {
	_, err := ParseVaultConfig([]byte(`stale_after_days = 30`))
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestDiscoverVaultRoot(t *testing.T) {
	tmp := t.TempDir()
	vault := filepath.Join(tmp, "myvault")
	sub := filepath.Join(vault, "concepts", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "mykb-wiki.toml"), []byte(`name = "main"`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverVaultRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	wantAbs, _ := filepath.Abs(vault)
	gotAbs, _ := filepath.Abs(got)
	if gotAbs != wantAbs {
		t.Errorf("got %q want %q", gotAbs, wantAbs)
	}
}

func TestDiscoverVaultRootNotFound(t *testing.T) {
	tmp := t.TempDir()
	_, err := DiscoverVaultRoot(tmp)
	if err == nil {
		t.Error("expected error when no vault found")
	}
}
```

- [ ] **Step 2: Run tests; verify they fail**

```bash
go test ./internal/wiki/ -run TestParseVaultConfig -v
```

Expected: build failure.

- [ ] **Step 3: Implement**

Create `internal/wiki/config.go`:

```go
package wiki

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// VaultConfigFilename is the well-known config file at the vault root.
const VaultConfigFilename = "mykb-wiki.toml"

// VaultConfig holds the per-vault configuration parsed from mykb-wiki.toml.
type VaultConfig struct {
	Name           string `toml:"name"`
	StaleAfterDays int    `toml:"stale_after_days"`
}

// ParseVaultConfig parses TOML bytes into a VaultConfig and applies defaults.
// Returns an error if `name` is missing.
func ParseVaultConfig(data []byte) (VaultConfig, error) {
	var cfg VaultConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return VaultConfig{}, fmt.Errorf("parse vault config: %w", err)
	}
	if cfg.Name == "" {
		return VaultConfig{}, fmt.Errorf("vault config missing required field: name")
	}
	if cfg.StaleAfterDays == 0 {
		cfg.StaleAfterDays = 180
	}
	return cfg, nil
}

// LoadVaultConfig reads and parses mykb-wiki.toml from the given vault root.
func LoadVaultConfig(vaultRoot string) (VaultConfig, error) {
	path := filepath.Join(vaultRoot, VaultConfigFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return VaultConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseVaultConfig(data)
}

// DiscoverVaultRoot walks up from `start` looking for a directory containing
// mykb-wiki.toml. Returns an absolute path to the vault root, or an error
// if no vault is found before reaching the filesystem root.
func DiscoverVaultRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, VaultConfigFilename)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in a wiki vault (no %s found from %s upward)", VaultConfigFilename, start)
		}
		dir = parent
	}
}
```

Verify TOML library: `grep pelletier/go-toml go.mod`. If absent: `go get github.com/pelletier/go-toml/v2`.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/wiki/ -v
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/config.go internal/wiki/config_test.go go.mod go.sum
git commit -m "feat: add vault config TOML and auto-discovery"
```

---

## Task 5: Postgres `ListWikiDocuments` and content-hash storage

**Files:**
- Modify: `internal/storage/postgres.go`
- Test: `internal/storage/postgres_test.go` (extend existing)

- [ ] **Step 1: Read the existing InsertDocument signature**

```bash
grep -n "func.*InsertDocument\|ContentHash\|content_hash" /Users/alepar/AleCode/mykb/internal/storage/postgres.go
```

Note the existing `InsertDocument(ctx, url) (Document, error)` signature for reference.

- [ ] **Step 2: Write the failing test**

Append to `internal/storage/postgres_test.go` (after existing tests; preserve imports):

```go
func TestListWikiDocuments(t *testing.T) {
	pg := setupTestPostgres(t)
	ctx := context.Background()

	// Seed: two wiki docs in "main", one in "personal", one raw source.
	main1, err := pg.UpsertWikiDocument(ctx, "wiki://main/concepts/foo.md", "Foo", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	main2, err := pg.UpsertWikiDocument(ctx, "wiki://main/entities/bar.md", "Bar", "def456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.UpsertWikiDocument(ctx, "wiki://personal/x.md", "X", "xyz"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.InsertDocument(ctx, "https://example.com/raw"); err != nil {
		t.Fatal(err)
	}

	got, err := pg.ListWikiDocuments(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 wiki docs in main, got %d", len(got))
	}
	urls := map[string]string{}
	for _, d := range got {
		urls[d.URL] = d.ContentHash
	}
	if urls[main1.URL] != "abc123" || urls[main2.URL] != "def456" {
		t.Errorf("unexpected hashes: %+v", urls)
	}
}

func TestUpsertWikiDocumentIdempotent(t *testing.T) {
	pg := setupTestPostgres(t)
	ctx := context.Background()

	doc1, err := pg.UpsertWikiDocument(ctx, "wiki://main/concepts/foo.md", "Foo", "hash-v1")
	if err != nil {
		t.Fatal(err)
	}

	doc2, err := pg.UpsertWikiDocument(ctx, "wiki://main/concepts/foo.md", "Foo Updated", "hash-v2")
	if err != nil {
		t.Fatal(err)
	}
	if doc1.ID != doc2.ID {
		t.Errorf("expected same ID on upsert, got %s then %s", doc1.ID, doc2.ID)
	}
	if doc2.ContentHash != "hash-v2" || doc2.Title != "Foo Updated" {
		t.Errorf("expected updated fields, got hash=%q title=%q", doc2.ContentHash, doc2.Title)
	}
}
```

If `setupTestPostgres` doesn't exist, look at existing tests in the file for the test-DB setup pattern and use it; reproduce the helper if needed (these tests assume one is already there — verify by reading the test file).

- [ ] **Step 3: Run tests; verify they fail**

```bash
go test ./internal/storage/ -run TestListWikiDocuments -v
```

Expected: build failure (`UpsertWikiDocument`, `ListWikiDocuments` undefined; `Document.ContentHash` field undefined).

- [ ] **Step 4: Add `ContentHash` field to the `Document` struct**

In `internal/storage/postgres.go`, find the `Document` struct definition (search: `type Document struct`). Add field:

```go
type Document struct {
	// ... existing fields ...
	ContentHash string // sha256 of body for wiki docs; empty for raw sources
}
```

Update any existing `Scan` / row-mapping helpers in this file that load `Document` values to also load `content_hash` (use `COALESCE(content_hash, '')`). Search for `SELECT.*FROM documents` and add `content_hash` to those projections. The exact lines depend on the current state of the file; the migration is mechanical.

- [ ] **Step 5: Implement `UpsertWikiDocument` and `ListWikiDocuments`**

Append to `internal/storage/postgres.go`:

```go
// UpsertWikiDocument inserts or updates a wiki document by URL. Used by the
// wiki ingest path. The unique constraint on documents.url ensures one row
// per URL; on conflict, title and content_hash are updated.
func (s *PostgresStore) UpsertWikiDocument(ctx context.Context, url, title, contentHash string) (Document, error) {
	const q = `
		INSERT INTO documents (url, title, content_hash, step, state)
		VALUES ($1, $2, $3, 'DONE', 'COMPLETED')
		ON CONFLICT (url) DO UPDATE SET
			title = EXCLUDED.title,
			content_hash = EXCLUDED.content_hash,
			updated_at = now()
		RETURNING id, url, COALESCE(title, ''), COALESCE(content_hash, ''), created_at, updated_at
	`
	var doc Document
	if err := s.db.QueryRowContext(ctx, q, url, title, contentHash).Scan(
		&doc.ID, &doc.URL, &doc.Title, &doc.ContentHash, &doc.CreatedAt, &doc.UpdatedAt,
	); err != nil {
		return Document{}, fmt.Errorf("upsert wiki document: %w", err)
	}
	return doc, nil
}

// ListWikiDocuments returns (URL, ContentHash) pairs for all wiki documents
// belonging to the given wiki name. Used by `mykb wiki sync` to compute the
// three-way diff against the local vault.
func (s *PostgresStore) ListWikiDocuments(ctx context.Context, wikiName string) ([]Document, error) {
	prefix := "wiki://" + wikiName + "/"
	const q = `
		SELECT id, url, COALESCE(title, ''), COALESCE(content_hash, '')
		FROM documents
		WHERE url LIKE $1
		ORDER BY url
	`
	rows, err := s.db.QueryContext(ctx, q, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("list wiki documents: %w", err)
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.ContentHash); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
```

Note: the `Document` struct field names (`UpdatedAt`, `CreatedAt`) must match what's already declared. If the field is `time.Time` vs `int64`, adjust accordingly. Read the existing struct to confirm.

- [ ] **Step 6: Run tests**

```bash
go test ./internal/storage/ -run "TestListWikiDocuments|TestUpsertWikiDocumentIdempotent" -v
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add internal/storage/postgres.go internal/storage/postgres_test.go
git commit -m "feat: add UpsertWikiDocument and ListWikiDocuments to postgres store"
```

---

## Task 6: Synchronous wiki ingest pipeline function

**Files:**
- Create: `internal/pipeline/wiki_ingest.go`
- Test: `internal/pipeline/wiki_ingest_test.go`

This function bypasses the worker. It is called inline by the `IngestMarkdown` server handler. It runs: strip frontmatter → chunk → embed → index → upsert document and chunk metadata.

- [ ] **Step 1: Write the failing test**

Create `internal/pipeline/wiki_ingest_test.go`:

```go
package pipeline

import (
	"context"
	"testing"
)

// TestStripFrontmatterBeforeChunking ensures the wiki ingest pipeline strips
// frontmatter before passing content to the chunker, so embeddings are clean.
func TestStripFrontmatterBeforeChunking(t *testing.T) {
	body := "---\ntype: concept\n---\n# Title\n\nBody text here."
	stripped := stripFrontmatterForChunking(body)
	if stripped != "# Title\n\nBody text here." {
		t.Errorf("got %q", stripped)
	}
}

// TestComputeContentHash ensures sha256 over the full body (frontmatter
// included) is what gets stored, so frontmatter changes trigger re-ingest.
func TestComputeContentHash(t *testing.T) {
	a := ComputeContentHash("---\nx: 1\n---\nBody")
	b := ComputeContentHash("---\nx: 2\n---\nBody")
	if a == b {
		t.Error("expected different hashes when frontmatter differs")
	}
	c := ComputeContentHash("---\nx: 1\n---\nBody")
	if a != c {
		t.Error("expected stable hash for identical input")
	}
}

// TestRunWikiIngestSkipsIfHashMatches: when the existing document's
// content_hash matches the new one, the pipeline returns a no-op result
// without re-embedding.
func TestRunWikiIngestSkipsIfHashMatches(t *testing.T) {
	t.Skip("integration test — covered by Task 8 server-level test against real stack")
	_ = context.Background()
}
```

- [ ] **Step 2: Run tests; verify they fail**

```bash
go test ./internal/pipeline/ -run "TestStripFrontmatter|TestComputeContentHash" -v
```

Expected: build failure.

- [ ] **Step 3: Implement**

Create `internal/pipeline/wiki_ingest.go`:

```go
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"mykb/internal/storage"
	"mykb/internal/wiki"
)

// ComputeContentHash returns the hex-encoded sha256 of the input.
// Used as the content_hash for wiki documents.
func ComputeContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// stripFrontmatterForChunking removes leading YAML frontmatter so it doesn't
// pollute embeddings. The original body (with frontmatter) is what gets
// stored on the document; only the embedded chunks see the stripped form.
func stripFrontmatterForChunking(body string) string {
	_, stripped := wiki.SplitFrontmatter(body)
	return strings.TrimLeft(stripped, "\n")
}

// WikiIngestResult summarizes the outcome of a wiki ingest call.
type WikiIngestResult struct {
	DocumentID string
	Chunks     int
	WasNoop    bool // true if content_hash matched and we skipped
}

// WikiIngestor runs the synchronous wiki ingest pipeline:
// strip frontmatter -> chunk -> embed -> index -> store.
type WikiIngestor struct {
	pg       *storage.PostgresStore
	embedder *Embedder
	indexer  *Indexer
}

// NewWikiIngestor wires the pieces. The caller owns lifecycle of the
// underlying stores and the embedder.
func NewWikiIngestor(pg *storage.PostgresStore, embedder *Embedder, indexer *Indexer) *WikiIngestor {
	return &WikiIngestor{pg: pg, embedder: embedder, indexer: indexer}
}

// Ingest runs the pipeline for a single wiki document. Idempotent: if a
// document with the given URL already exists with a matching content_hash,
// returns a no-op result without re-embedding.
//
// The url MUST be a wiki:// URL. The body is the full markdown, including
// frontmatter — the function strips it before chunking. The caller has
// already computed `contentHash` from the same body.
func (w *WikiIngestor) Ingest(ctx context.Context, url, title, body, contentHash string) (WikiIngestResult, error) {
	if !wiki.IsWikiURL(url) {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest: not a wiki URL: %q", url)
	}

	// Idempotency: if the existing doc has the same hash, skip everything.
	if existing, err := w.pg.GetDocumentByURL(ctx, url); err == nil && existing.ContentHash == contentHash && contentHash != "" {
		return WikiIngestResult{DocumentID: existing.ID, Chunks: existing.ChunkCount, WasNoop: true}, nil
	}

	// Upsert document row (creates or updates by URL). Title and
	// content_hash are updated; chunks are replaced below.
	doc, err := w.pg.UpsertWikiDocument(ctx, url, title, contentHash)
	if err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest upsert: %w", err)
	}

	// Replace any pre-existing chunks for this document across all stores.
	// (Postgres FK cascades chunks; we still must clear Qdrant + Meilisearch.)
	if err := w.indexer.qdrant.DeleteByDocumentID(ctx, doc.ID); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest clear qdrant: %w", err)
	}
	if err := w.indexer.meilisearch.DeleteByDocumentID(ctx, doc.ID); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest clear meilisearch: %w", err)
	}
	if err := w.pg.DeleteChunks(ctx, doc.ID); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest clear chunks: %w", err)
	}

	// Chunk (frontmatter stripped).
	chunkedText := ChunkMarkdown(stripFrontmatterForChunking(body), ChunkOptions{}.withDefaults())
	if len(chunkedText) == 0 {
		return WikiIngestResult{DocumentID: doc.ID, Chunks: 0}, nil
	}

	// Embed.
	vectors, err := w.embedder.EmbedChunks(ctx, chunkedText)
	if err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest embed: %w", err)
	}

	// Persist chunks (Postgres) and build index payloads.
	indexable := make([]IndexableChunk, len(chunkedText))
	for i, txt := range chunkedText {
		chunkID, err := w.pg.InsertChunk(ctx, doc.ID, i)
		if err != nil {
			return WikiIngestResult{}, fmt.Errorf("wiki ingest insert chunk: %w", err)
		}
		indexable[i] = IndexableChunk{
			ID:         chunkID,
			DocumentID: doc.ID,
			ChunkIndex: i,
			Vector:     vectors[i],
			Text:       txt,
		}
	}

	// Index.
	if err := w.indexer.IndexChunks(ctx, indexable); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest index: %w", err)
	}

	// Update chunk_count on the document.
	if err := w.pg.SetDocumentChunkCount(ctx, doc.ID, len(chunkedText)); err != nil {
		return WikiIngestResult{}, fmt.Errorf("wiki ingest set chunk count: %w", err)
	}

	return WikiIngestResult{DocumentID: doc.ID, Chunks: len(chunkedText)}, nil
}
```

This depends on three storage methods that may need adding/verifying in postgres.go:
- `GetDocumentByURL(ctx, url) (Document, error)` — likely exists (search).
- `DeleteChunks(ctx, docID)` — may not exist; if absent, add a wrapper around `DELETE FROM chunks WHERE document_id = $1`.
- `InsertChunk(ctx, docID, idx) (chunkID string, error)` — likely exists for the worker; verify.
- `SetDocumentChunkCount(ctx, docID, n int)` — search; if absent, add it.

For each missing method, write a small test and implement it as a sub-step. Pattern: read existing similar method, mirror it.

- [ ] **Step 4: Verify storage methods exist; add missing ones**

```bash
grep -n "GetDocumentByURL\|DeleteChunks\|InsertChunk\|SetDocumentChunkCount\|ChunkCount" /Users/alepar/AleCode/mykb/internal/storage/postgres.go
```

For any missing methods, add them following the existing pattern. Concrete implementations (add only the ones absent):

```go
// DeleteChunks removes all chunks for a document. Use before re-inserting.
func (s *PostgresStore) DeleteChunks(ctx context.Context, documentID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	return nil
}

// SetDocumentChunkCount updates the cached chunk count on a document row.
func (s *PostgresStore) SetDocumentChunkCount(ctx context.Context, documentID string, n int) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE documents SET chunk_count = $1, updated_at = now() WHERE id = $2`, n, documentID); err != nil {
		return fmt.Errorf("set chunk count: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/pipeline/ ./internal/storage/ -v
```

Expected: pass (the table-driven unit tests in `wiki_ingest_test.go` plus all pre-existing tests).

- [ ] **Step 6: Commit**

```bash
git add internal/pipeline/wiki_ingest.go internal/pipeline/wiki_ingest_test.go internal/storage/postgres.go
git commit -m "feat: add synchronous wiki ingest pipeline function"
```

---

## Task 7: Proto definitions for `IngestMarkdown` and `ListWikiDocuments`

**Files:**
- Modify: `proto/mykb/v1/kb.proto`

- [ ] **Step 1: Add the new RPCs and messages**

Edit `proto/mykb/v1/kb.proto`. Add to the service:

```proto
service KBService {
  rpc IngestURL(IngestURLRequest) returns (stream IngestProgress);
  rpc IngestURLs(IngestURLsRequest) returns (stream IngestURLsProgress);
  rpc Query(QueryRequest) returns (QueryResponse);
  rpc ListDocuments(ListDocumentsRequest) returns (ListDocumentsResponse);
  rpc GetDocuments(GetDocumentsRequest) returns (GetDocumentsResponse);
  rpc DeleteDocument(DeleteDocumentRequest) returns (DeleteDocumentResponse);

  // Wiki ingest (synchronous; bypasses crawler since the body is already markdown).
  rpc IngestMarkdown(IngestMarkdownRequest) returns (IngestMarkdownResponse);
  // List documents belonging to a given wiki, returning URL + content_hash for sync diffing.
  rpc ListWikiDocuments(ListWikiDocumentsRequest) returns (ListWikiDocumentsResponse);
}
```

Append message definitions at the end of the file:

```proto
message IngestMarkdownRequest {
  string url = 1;          // synthetic, e.g. wiki://main/concepts/foo.md
  string title = 2;        // optional; otherwise extracted by the server
  string body = 3;         // markdown including frontmatter
  string content_hash = 4; // sha256 of body, for idempotent re-ingest
}

message IngestMarkdownResponse {
  string document_id = 1;
  int32 chunks = 2;
  bool was_noop = 3;       // true if content_hash matched the existing doc and we skipped
}

message ListWikiDocumentsRequest {
  string wiki_name = 1;
}

message ListWikiDocumentsResponse {
  repeated WikiDocument documents = 1;
}

message WikiDocument {
  string url = 1;
  string content_hash = 2;
}
```

- [ ] **Step 2: Regenerate ConnectRPC code**

```bash
just proto
```

Expected: files under `gen/mykb/v1/` updated; no errors.

- [ ] **Step 3: Verify generated code compiles**

```bash
go build ./...
```

Expected: success. The new server handlers don't exist yet — but the generated interface includes them. The build will fail at `internal/server/server.go` because `Server` doesn't implement `IngestMarkdown` or `ListWikiDocuments`. That's expected; Task 8 implements them.

- [ ] **Step 4: Commit (proto only — handlers come next)**

```bash
git add proto/mykb/v1/kb.proto gen/mykb/v1/
git commit -m "feat: add IngestMarkdown and ListWikiDocuments to proto"
```

---

## Task 8: Server handlers for `IngestMarkdown` and `ListWikiDocuments`

**Files:**
- Modify: `internal/server/server.go`

- [ ] **Step 1: Add a `WikiIngestor` to the `Server` struct and constructor**

Read `internal/server/server.go` lines around `NewServer` to locate the struct and constructor. Add a field and a constructor parameter:

```go
type Server struct {
	// ... existing fields ...
	wikiIngestor *pipeline.WikiIngestor
}

func NewServer(
	// ... existing parameters ...
	wikiIngestor *pipeline.WikiIngestor,
) *Server {
	return &Server{
		// ... existing assignments ...
		wikiIngestor: wikiIngestor,
	}
}
```

- [ ] **Step 2: Update the construction site (`cmd/mykb-api/main.go` or wherever `NewServer` is called)**

```bash
grep -rn "NewServer(" /Users/alepar/AleCode/mykb/ --include="*.go"
```

Find the wiring location and pass `pipeline.NewWikiIngestor(pg, embedder, indexer)`. The `embedder` and `indexer` are already constructed nearby for the worker; reuse them.

Example wiring (adapt to actual variable names):

```go
wikiIngestor := pipeline.NewWikiIngestor(pgStore, embedder, indexer)
srv := server.NewServer(/* ...existing... */, wikiIngestor)
```

- [ ] **Step 3: Implement `IngestMarkdown`**

Append to `internal/server/server.go`:

```go
// IngestMarkdown ingests a single wiki document synchronously. The body
// includes frontmatter; the pipeline strips it before chunking. Idempotent
// on (url, content_hash).
func (s *Server) IngestMarkdown(ctx context.Context, req *connect.Request[mykbv1.IngestMarkdownRequest]) (*connect.Response[mykbv1.IngestMarkdownResponse], error) {
	url := req.Msg.GetUrl()
	if !wiki.IsWikiURL(url) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("url must use wiki:// scheme: %q", url))
	}
	body := req.Msg.GetBody()
	hash := req.Msg.GetContentHash()
	if hash == "" {
		hash = pipeline.ComputeContentHash(body)
	}

	title := req.Msg.GetTitle()
	if title == "" {
		_, vaultPath, _ := wiki.URLToVaultPath(url)
		title = wiki.ExtractTitle(body, vaultPath)
	}

	res, err := s.wikiIngestor.Ingest(ctx, url, title, body, hash)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&mykbv1.IngestMarkdownResponse{
		DocumentId: res.DocumentID,
		Chunks:     int32(res.Chunks),
		WasNoop:    res.WasNoop,
	}), nil
}
```

Add the new imports if not already present: `"mykb/internal/wiki"`, `"mykb/internal/pipeline"`.

- [ ] **Step 4: Implement `ListWikiDocuments`**

```go
// ListWikiDocuments returns (url, content_hash) for all wiki documents
// belonging to the given wiki. Used by `mykb wiki sync` for diffing.
func (s *Server) ListWikiDocuments(ctx context.Context, req *connect.Request[mykbv1.ListWikiDocumentsRequest]) (*connect.Response[mykbv1.ListWikiDocumentsResponse], error) {
	wikiName := req.Msg.GetWikiName()
	if wikiName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("wiki_name is required"))
	}
	docs, err := s.pg.ListWikiDocuments(ctx, wikiName)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list wiki documents: %v", err))
	}
	out := make([]*mykbv1.WikiDocument, 0, len(docs))
	for _, d := range docs {
		out = append(out, &mykbv1.WikiDocument{
			Url:         d.URL,
			ContentHash: d.ContentHash,
		})
	}
	return connect.NewResponse(&mykbv1.ListWikiDocumentsResponse{Documents: out}), nil
}
```

- [ ] **Step 5: Build**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 6: Smoke-test against a running server**

```bash
just up   # ensure the API is up with the new code
# Issue an IngestMarkdown via curl-equivalent. Easiest: Connect's HTTP API.
curl -X POST 'http://localhost:9091/mykb.v1.KBService/IngestMarkdown' \
  -H 'Content-Type: application/json' \
  -d '{"url":"wiki://test/concepts/smoke.md","title":"Smoke","body":"---\ntype: concept\n---\n# Smoke\n\nHello.","content_hash":""}'
```

Expected: `{"document_id":"<uuid>","chunks":1,"was_noop":false}`.

Then run the same call again:

Expected: `{"document_id":"<same uuid>","chunks":1,"was_noop":true}`.

Clean up: `curl -X POST 'http://localhost:9091/mykb.v1.KBService/DeleteDocument' -H 'Content-Type: application/json' -d '{"id":"<uuid>"}'`.

- [ ] **Step 7: Commit**

```bash
git add internal/server/server.go cmd/mykb-api/  # adjust path if NewServer caller is elsewhere
git commit -m "feat: add IngestMarkdown and ListWikiDocuments server handlers"
```

---

## Task 9: Wikilink parser

**Files:**
- Create: `internal/wiki/wikilinks.go`
- Test: `internal/wiki/wikilinks_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/wiki/wikilinks_test.go`:

```go
package wiki

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseWikilinks(t *testing.T) {
	tests := []struct {
		name, in string
		want     []Wikilink
	}{
		{
			"basic",
			"See [[voyage-context-3]] for details.",
			[]Wikilink{{Target: "voyage-context-3", Label: ""}},
		},
		{
			"with_label",
			"See [[wiki://main/entities/foo.md|foo]] now.",
			[]Wikilink{{Target: "wiki://main/entities/foo.md", Label: "foo"}},
		},
		{
			"multiple",
			"[[a]] and [[b|alias]] and [[c]].",
			[]Wikilink{
				{Target: "a", Label: ""},
				{Target: "b", Label: "alias"},
				{Target: "c", Label: ""},
			},
		},
		{
			"inside_code_fence_ignored",
			"Use ```\n[[not_a_link]]\n``` for that.",
			nil,
		},
		{
			"inline_code_ignored",
			"Use `[[not_a_link]]` carefully.",
			nil,
		},
		{
			"escaped_brackets_ignored",
			`See \[\[not\_a\_link\]\] documented.`,
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseWikilinks(tt.in)
			sort.Slice(got, func(i, j int) bool { return got[i].Target < got[j].Target })
			sort.Slice(tt.want, func(i, j int) bool { return tt.want[i].Target < tt.want[j].Target })
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run; verify failure**

```bash
go test ./internal/wiki/ -run TestParseWikilinks -v
```

Expected: build failure.

- [ ] **Step 3: Implement**

Create `internal/wiki/wikilinks.go`:

```go
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
		if strings.HasPrefix(trimmed, "```") {
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
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/wiki/ -v
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/wikilinks.go internal/wiki/wikilinks_test.go
git commit -m "feat: add wikilink parser tolerant of code fences and escapes"
```

---

## Task 10: Lint checks

**Files:**
- Create: `internal/wiki/lint.go`
- Test: `internal/wiki/lint_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/wiki/lint_test.go`:

```go
package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintMissingFrontmatter(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml":     `name = "main"`,
		"concepts/no-fm.md":  "# A page\nbody",
		"entities/ok.md":     "---\ntype: entity\nkind: tool\ndate_updated: 2026-04-30\n---\n# OK\n",
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
		"mykb-wiki.toml":      `name = "main"`,
		"concepts/a.md":       "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# A\nSee [[missing-page]].",
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
		"mykb-wiki.toml":  `name = "main"`,
		"concepts/a.md":   "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# A\n",
		"concepts/b.md":   "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# B\nSee [[a]].",
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
```

- [ ] **Step 2: Run; verify failure**

```bash
go test ./internal/wiki/ -run "TestLint" -v
```

Expected: build failure.

- [ ] **Step 3: Implement**

Create `internal/wiki/lint.go`:

```go
package wiki

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LintFinding is a single lint result.
type LintFinding struct {
	Path    string
	Message string
}

// LintReport groups findings by severity. Errors fail the lint command;
// warnings always print but exit 0 if no errors.
type LintReport struct {
	Errors   []LintFinding
	Warnings []LintFinding
}

// Lint walks the vault rooted at vaultRoot and runs all checks.
func Lint(vaultRoot string) (LintReport, error) {
	cfg, err := LoadVaultConfig(vaultRoot)
	if err != nil {
		return LintReport{}, err
	}

	pages, err := walkVaultMarkdown(vaultRoot)
	if err != nil {
		return LintReport{}, err
	}

	var report LintReport

	// Build name -> page map for short-form wikilink resolution.
	byName := map[string]*vaultPage{}
	for i, p := range pages {
		stem := stemOf(p.relPath)
		byName[stem] = &pages[i]
		// Also register frontmatter aliases (entities only).
		for _, a := range p.aliases {
			byName[a] = &pages[i]
		}
	}

	now := time.Now()
	staleThreshold := time.Duration(cfg.StaleAfterDays) * 24 * time.Hour
	inboundCount := map[string]int{}

	for _, p := range pages {
		// Check 1: required frontmatter.
		if errs := validateFrontmatter(p); len(errs) > 0 {
			for _, e := range errs {
				report.Errors = append(report.Errors, LintFinding{Path: p.relPath, Message: e})
			}
		}

		// Check 2: broken wikilinks.
		for _, link := range p.links {
			target := link.Target
			if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") {
				continue
			}
			if strings.HasPrefix(target, URLScheme) {
				_, vp, err := URLToVaultPath(target)
				if err != nil || !pathExists(vaultRoot, vp) {
					report.Errors = append(report.Errors, LintFinding{
						Path:    p.relPath,
						Message: fmt.Sprintf("broken wikilink: %s", target),
					})
				}
				continue
			}
			// Short form: resolve by stem or alias.
			resolved, ok := byName[target]
			if !ok {
				report.Errors = append(report.Errors, LintFinding{
					Path:    p.relPath,
					Message: fmt.Sprintf("broken wikilink: [[%s]] (no matching page)", target),
				})
				continue
			}
			inboundCount[resolved.relPath]++
		}

		// Check 4: stale.
		if !p.dateUpdated.IsZero() && p.frontmatter["type"] != "synthesis" {
			if now.Sub(p.dateUpdated) > staleThreshold {
				days := int(now.Sub(p.dateUpdated).Hours() / 24)
				report.Warnings = append(report.Warnings, LintFinding{
					Path:    p.relPath,
					Message: fmt.Sprintf("stale (date_updated %s, %d days ago)", p.dateUpdated.Format("2006-01-02"), days),
				})
			}
		}
	}

	// Check 3: orphans (after we've counted inbound for all pages).
	for _, p := range pages {
		if inboundCount[p.relPath] == 0 {
			// Synthesis pages explicitly superseded are exempt.
			if sup, _ := p.frontmatter["superseded_by"].(string); sup != "" {
				continue
			}
			report.Warnings = append(report.Warnings, LintFinding{
				Path:    p.relPath,
				Message: "orphan (no inbound wikilinks)",
			})
		}
	}

	return report, nil
}

type vaultPage struct {
	relPath     string
	frontmatter map[string]any
	body        string
	links       []Wikilink
	aliases     []string
	dateUpdated time.Time
}

func walkVaultMarkdown(root string) ([]vaultPage, error) {
	var out []vaultPage
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip excluded directories.
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == ".templates" {
				if path != root {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		base := filepath.Base(rel)
		if base == "Log.md" || base == "CLAUDE.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmStr, body := SplitFrontmatter(string(data))
		fm, _ := ParseFrontmatter(fmStr)
		page := vaultPage{
			relPath:     filepath.ToSlash(rel),
			frontmatter: fm,
			body:        body,
			links:       ParseWikilinks(body),
		}
		if aliases, ok := fm["aliases"].([]any); ok {
			for _, a := range aliases {
				if s, ok := a.(string); ok {
					page.aliases = append(page.aliases, s)
				}
			}
		}
		if du, ok := fm["date_updated"].(string); ok {
			if t, err := time.Parse("2006-01-02", du); err == nil {
				page.dateUpdated = t
			}
		}
		out = append(out, page)
		return nil
	})
	return out, err
}

func validateFrontmatter(p vaultPage) []string {
	var errs []string
	t, _ := p.frontmatter["type"].(string)
	switch t {
	case "":
		errs = append(errs, "missing required frontmatter field: type")
	case "entity", "concept", "synthesis":
	default:
		errs = append(errs, fmt.Sprintf("invalid frontmatter type: %q (expected entity|concept|synthesis)", t))
	}
	if _, ok := p.frontmatter["date_updated"].(string); !ok {
		errs = append(errs, "missing required frontmatter field: date_updated")
	}
	if t == "entity" {
		if _, ok := p.frontmatter["kind"].(string); !ok {
			errs = append(errs, "entity missing required field: kind")
		}
	}
	if t == "synthesis" {
		if _, ok := p.frontmatter["question"].(string); !ok {
			errs = append(errs, "synthesis missing required field: question")
		}
		if _, ok := p.frontmatter["answered_at"].(string); !ok {
			errs = append(errs, "synthesis missing required field: answered_at")
		}
	}
	return errs
}

func stemOf(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, ".md")
}

func pathExists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/wiki/ -v
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/lint.go internal/wiki/lint_test.go
git commit -m "feat: add wiki vault lint checks (frontmatter, links, orphans, stale)"
```

---

## Task 11: CLI scaffolding for `mykb wiki` dispatcher

**Files:**
- Create: `cmd/mykb/wiki.go`
- Modify: `cmd/mykb/main.go`

- [ ] **Step 1: Add the `wiki` route in `main.go`**

Edit `cmd/mykb/main.go`. Find the `switch os.Args[1]` block (around line 30). Add a case:

```go
	case "wiki":
		runWiki(os.Args[2:])
```

Update `printUsage()` to include:

```go
	fmt.Fprintln(os.Stderr, "  mykb wiki <subcommand> [args]   (init|sync|ingest|list|lint)")
```

- [ ] **Step 2: Create the dispatcher**

Create `cmd/mykb/wiki.go`:

```go
package main

import (
	"fmt"
	"os"
)

func runWiki(args []string) {
	if len(args) < 1 {
		printWikiUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "init":
		runWikiInit(args[1:])
	case "sync":
		runWikiSync(args[1:])
	case "ingest":
		runWikiIngest(args[1:])
	case "list":
		runWikiList(args[1:])
	case "lint":
		runWikiLint(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown wiki subcommand: %s\n", args[0])
		printWikiUsage()
		os.Exit(1)
	}
}

func printWikiUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  mykb wiki init [--vault DIR]                  scaffold a new wiki vault")
	fmt.Fprintln(os.Stderr, "  mykb wiki sync [--vault DIR] [--host HOST]    sync vault with mykb (diff-based)")
	fmt.Fprintln(os.Stderr, "  mykb wiki ingest <file> [--vault DIR] [--host HOST]   ingest a single file")
	fmt.Fprintln(os.Stderr, "  mykb wiki list [--vault DIR]                  list vault inventory")
	fmt.Fprintln(os.Stderr, "  mykb wiki lint [--vault DIR]                  validate vault structure")
}

// resolveVault returns the vault root, either from --vault or by walking up from cwd.
func resolveVault(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wiki.DiscoverVaultRoot(cwd)
}
```

(Note: `wiki` import added in subsequent tasks; this file currently doesn't compile until `runWikiInit` etc. exist. Add the import now: `"mykb/internal/wiki"`. The stub functions are placeholders that the next tasks will define.)

- [ ] **Step 3: Add stub functions to keep the build green**

Append to `cmd/mykb/wiki.go`:

```go
func runWikiInit(args []string)   { fmt.Fprintln(os.Stderr, "wiki init: not yet implemented"); os.Exit(2) }
func runWikiSync(args []string)   { fmt.Fprintln(os.Stderr, "wiki sync: not yet implemented"); os.Exit(2) }
func runWikiIngest(args []string) { fmt.Fprintln(os.Stderr, "wiki ingest: not yet implemented"); os.Exit(2) }
func runWikiList(args []string)   { fmt.Fprintln(os.Stderr, "wiki list: not yet implemented"); os.Exit(2) }
func runWikiLint(args []string)   { fmt.Fprintln(os.Stderr, "wiki lint: not yet implemented"); os.Exit(2) }
```

These will be replaced one-by-one in following tasks.

- [ ] **Step 4: Build and verify**

```bash
go build ./...
just cli   # build the CLI binary
./mykb wiki
```

Expected: usage printed, exit 1. `./mykb wiki init` prints "not yet implemented", exit 2.

- [ ] **Step 5: Commit**

```bash
git add cmd/mykb/main.go cmd/mykb/wiki.go
git commit -m "feat: add mykb wiki subcommand dispatcher with stubs"
```

---

## Task 12: `mykb wiki init`

**Files:**
- Modify: `cmd/mykb/wiki.go`
- Modify: `internal/wiki/config.go` (or new file `internal/wiki/scaffold.go`) — add scaffolding helpers.

- [ ] **Step 1: Write a small test for the scaffold helper**

Append to `internal/wiki/config_test.go`:

```go
func TestScaffoldVault(t *testing.T) {
	dir := t.TempDir()
	if err := ScaffoldVault(dir, "main"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"mykb-wiki.toml",
		"CLAUDE.md",
		"Log.md",
		"entities/.gitkeep",
		"concepts/.gitkeep",
		"synthesis/.gitkeep",
		".templates/entity.md",
		".templates/concept.md",
		".templates/synthesis.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}
```

- [ ] **Step 2: Run; expect failure**

```bash
go test ./internal/wiki/ -run TestScaffoldVault -v
```

Expected: undefined symbol.

- [ ] **Step 3: Implement `ScaffoldVault`**

Create `internal/wiki/scaffold.go`:

```go
package wiki

import (
	"fmt"
	"os"
	"path/filepath"
)

// ScaffoldVault writes the initial vault layout into `dir`. It creates:
//   - mykb-wiki.toml (with the given wiki name)
//   - CLAUDE.md (the wiki operating manual)
//   - Log.md (with header)
//   - entities/, concepts/, synthesis/ (with .gitkeep)
//   - .templates/{entity,concept,synthesis}.md
// Returns an error if any required file already exists.
func ScaffoldVault(dir, wikiName string) error {
	if wikiName == "" {
		return fmt.Errorf("wiki name is empty")
	}

	files := map[string]string{
		"mykb-wiki.toml":           fmt.Sprintf("name = %q\nstale_after_days = 180\n", wikiName),
		"CLAUDE.md":                vaultClaudeMD(wikiName),
		"Log.md":                   "# Log\n\n",
		"entities/.gitkeep":        "",
		"concepts/.gitkeep":        "",
		"synthesis/.gitkeep":       "",
		".templates/entity.md":     templateEntity,
		".templates/concept.md":    templateConcept,
		".templates/synthesis.md":  templateSynthesis,
	}

	for path, content := range files {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); err == nil {
			return fmt.Errorf("file already exists: %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func vaultClaudeMD(wikiName string) string {
	return fmt.Sprintf(`# Wiki schema and operating rules — %s

This is a curated knowledge wiki. mykb handles search; this file tells you
how to maintain the vault.

## Layout
- entities/  — specific things (people, orgs, tools, models, repos)
- concepts/  — abstractions, techniques, patterns
- synthesis/ — answers to specific questions (write-once; supersede on change)
- Log.md     — append-only audit trail

## When to create which page type
- Single proper noun as the subject -> entity.
- A *kind of thing* -> concept.
- An answer to a question -> synthesis.
- Unsure between entity/concept -> prefer concept.

## Required frontmatter
Every page MUST have:
  type:         (entity | concept | synthesis)
  date_updated: (YYYY-MM-DD)

Entity pages also require:    kind:
Synthesis pages also require: question:, answered_at:

## Wikilinks
Use [[name]] for short links (resolves by filename or aliases).
Use [[wiki://%s/path/to/page.md|label]] for explicit URL or external links.

## Workflow
1. Search mykb first ('mykb query "..."') to find existing relevant pages.
2. Read the full pages mykb returns (frontmatter + body).
3. If a page already covers the topic, update it (bump date_updated).
   Otherwise, create a new page in the right directory.
4. After every file write, run 'mykb wiki ingest <relative-path>'.
5. Append to Log.md a line like:
   YYYY-MM-DD HH:MM <verb> <type> <path> [from sources X, Y]
6. Periodically run 'mykb wiki lint' and address findings.

## Synthesis pages
Never edit an existing synthesis page to change the answer. Instead:
- Write a new synthesis page with the updated answer.
- Set the old page's superseded_by: to the new page's path.
- Mention the supersession in the new page's body and in Log.md.

## What goes in mykb directly (raw sources)
Web pages, papers, docs -- ingest with 'mykb ingest <url>'. They are NOT
copied into the vault; wiki:// URLs are vault-only. Synthesis pages link
to raw sources by their https:// URLs.

## Templates
See .templates/entity.md, .templates/concept.md, .templates/synthesis.md.
`, wikiName, wikiName)
}

const templateEntity = `---
type: entity
kind: tool                 # person | org | tool | model | repo | dataset | product
aliases: []
homepage:
date_updated: YYYY-MM-DD
---

# <Entity Name>

One-paragraph description.

## Properties
- Producer: [[<Org>]]
- ...

## Notes
`

const templateConcept = `---
type: concept
confidence: medium         # low | medium | high
related: []
date_updated: YYYY-MM-DD
---

# <Concept Name>

One-paragraph definition.

## Details
`

const templateSynthesis = `---
type: synthesis
question: "<the question>"
answered_at: YYYY-MM-DD
superseded_by: null
sources: []
---

# <The question>

Short answer first, then reasoning.
`
```

- [ ] **Step 4: Run scaffold test**

```bash
go test ./internal/wiki/ -run TestScaffoldVault -v
```

Expected: pass.

- [ ] **Step 5: Wire `runWikiInit`**

Replace the stub in `cmd/mykb/wiki.go`:

```go
func runWikiInit(args []string) {
	fs := flag.NewFlagSet("wiki init", flag.ExitOnError)
	dir := fs.String("vault", ".", "directory to scaffold the vault in")
	name := fs.String("name", "", "wiki name (will appear in URL prefix wiki://<name>/...)")
	fs.Parse(args)

	wikiName := *name
	if wikiName == "" {
		fmt.Fprint(os.Stderr, "Wiki name: ")
		var s string
		fmt.Fscanln(os.Stdin, &s)
		wikiName = strings.TrimSpace(s)
	}
	if wikiName == "" {
		fmt.Fprintln(os.Stderr, "wiki name is required")
		os.Exit(1)
	}
	if err := wiki.ScaffoldVault(*dir, wikiName); err != nil {
		fmt.Fprintf(os.Stderr, "scaffold failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("scaffolded wiki %q in %s\n", wikiName, *dir)
}
```

Add imports: `"flag"`, `"strings"`.

- [ ] **Step 6: Smoke test**

```bash
just cli
mkdir /tmp/myvault && ./mykb wiki init --vault /tmp/myvault --name testwiki
ls -la /tmp/myvault
cat /tmp/myvault/mykb-wiki.toml
rm -rf /tmp/myvault
```

Expected: vault scaffolded with the listed files; config contains `name = "testwiki"`.

- [ ] **Step 7: Commit**

```bash
git add internal/wiki/scaffold.go internal/wiki/config_test.go cmd/mykb/wiki.go
git commit -m "feat: add mykb wiki init for vault scaffolding"
```

---

## Task 13: `mykb wiki ingest <file>`

**Files:**
- Modify: `cmd/mykb/wiki.go`

- [ ] **Step 1: Replace the `runWikiIngest` stub**

```go
func runWikiIngest(args []string) {
	fs := flag.NewFlagSet("wiki ingest", flag.ExitOnError)
	vaultOverride := fs.String("vault", "", "vault root (default: auto-discover)")
	host := fs.String("host", "", "server address (default: from config)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: mykb wiki ingest <file> [--vault DIR] [--host HOST]")
		os.Exit(1)
	}
	relOrAbs := fs.Arg(0)

	vaultRoot, err := resolveVault(*vaultOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	cfg, err := wiki.LoadVaultConfig(vaultRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// Resolve the file: accept abs path, vault-relative path, or cwd-relative path.
	abs := relOrAbs
	if !filepath.IsAbs(abs) {
		// Try cwd first, then vault-root.
		if _, err := os.Stat(abs); err != nil {
			abs = filepath.Join(vaultRoot, relOrAbs)
		}
	}
	abs, err = filepath.Abs(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve path: %v\n", err)
		os.Exit(1)
	}
	rel, err := filepath.Rel(vaultRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		fmt.Fprintf(os.Stderr, "file %s is outside vault %s\n", abs, vaultRoot)
		os.Exit(1)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", abs, err)
		os.Exit(1)
	}

	url, err := wiki.VaultPathToURL(cfg.Name, filepath.ToSlash(rel))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	clientCfg := cliconfig.Load("")
	if *host != "" {
		clientCfg.Host = *host
	}
	client := mykbv1connect.NewKBServiceClient(http.DefaultClient, clientCfg.Host)

	hash := pipelineComputeContentHash(string(body))
	resp, err := client.IngestMarkdown(context.Background(), connect.NewRequest(&mykbv1.IngestMarkdownRequest{
		Url:         url,
		Title:       "",
		Body:        string(body),
		ContentHash: hash,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ingest: %v\n", err)
		os.Exit(1)
	}
	if resp.Msg.GetWasNoop() {
		fmt.Printf("noop: %s (unchanged)\n", url)
	} else {
		fmt.Printf("ingested: %s (%d chunks)\n", url, resp.Msg.GetChunks())
	}
}
```

The hash helper used here keeps the CLI from depending on `internal/pipeline` (which pulls server-side deps). Add a small standalone helper at the bottom of `cmd/mykb/wiki.go`:

```go
// pipelineComputeContentHash mirrors pipeline.ComputeContentHash without the dep.
func pipelineComputeContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
```

Imports to add: `"context"`, `"crypto/sha256"`, `"encoding/hex"`, `"filepath"` (already), `"net/http"`, `"connectrpc.com/connect"`, `mykbv1 "mykb/gen/mykb/v1"`, `"mykb/gen/mykb/v1/mykbv1connect"`, `"mykb/internal/cliconfig"`.

- [ ] **Step 2: Build and smoke test**

```bash
just cli
mkdir /tmp/wikitest && cd /tmp/wikitest
~/AleCode/mykb/mykb wiki init --name test
echo "---
type: concept
date_updated: 2026-04-30
---
# Smoke test concept

Some text." > concepts/smoke.md
~/AleCode/mykb/mykb wiki ingest concepts/smoke.md
~/AleCode/mykb/mykb wiki ingest concepts/smoke.md   # second run = noop
```

Expected: first run prints `ingested: wiki://test/concepts/smoke.md (1 chunks)`. Second run prints `noop: wiki://test/concepts/smoke.md (unchanged)`.

Clean up: `cd ~ && rm -rf /tmp/wikitest`. Also delete the test document via the API or by hitting `/healthz/deep`-style cleanup if available.

- [ ] **Step 3: Commit**

```bash
git add cmd/mykb/wiki.go
git commit -m "feat: implement mykb wiki ingest <file>"
```

---

## Task 14: `mykb wiki sync`

**Files:**
- Modify: `cmd/mykb/wiki.go`

- [ ] **Step 1: Implement the sync flow**

Replace the `runWikiSync` stub:

```go
func runWikiSync(args []string) {
	fs := flag.NewFlagSet("wiki sync", flag.ExitOnError)
	vaultOverride := fs.String("vault", "", "vault root (default: auto-discover)")
	host := fs.String("host", "", "server address (default: from config)")
	fs.Parse(args)

	vaultRoot, err := resolveVault(*vaultOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	cfg, err := wiki.LoadVaultConfig(vaultRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// Walk vault.
	type local struct {
		relPath string
		hash    string
	}
	var locals []local
	err = filepath.WalkDir(vaultRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != vaultRoot && (strings.HasPrefix(name, ".") || name == ".templates") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(vaultRoot, path)
		base := filepath.Base(rel)
		if base == "Log.md" || base == "CLAUDE.md" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		locals = append(locals, local{
			relPath: filepath.ToSlash(rel),
			hash:    pipelineComputeContentHash(string(body)),
		})
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk vault: %v\n", err)
		os.Exit(1)
	}

	// List remote.
	clientCfg := cliconfig.Load("")
	if *host != "" {
		clientCfg.Host = *host
	}
	client := mykbv1connect.NewKBServiceClient(http.DefaultClient, clientCfg.Host)
	listResp, err := client.ListWikiDocuments(context.Background(), connect.NewRequest(&mykbv1.ListWikiDocumentsRequest{WikiName: cfg.Name}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "list wiki documents: %v\n", err)
		os.Exit(1)
	}
	remote := map[string]string{}
	for _, d := range listResp.Msg.GetDocuments() {
		remote[d.GetUrl()] = d.GetContentHash()
	}

	// Diff.
	var added, changed, deleted int
	seen := map[string]bool{}
	for _, l := range locals {
		url, _ := wiki.VaultPathToURL(cfg.Name, l.relPath)
		seen[url] = true
		body, err := os.ReadFile(filepath.Join(vaultRoot, l.relPath))
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", l.relPath, err)
			continue
		}
		existingHash, ok := remote[url]
		switch {
		case !ok:
			if err := callIngest(client, url, string(body), l.hash); err != nil {
				fmt.Fprintf(os.Stderr, "ingest %s: %v\n", url, err)
				continue
			}
			added++
		case existingHash != l.hash:
			if err := callIngest(client, url, string(body), l.hash); err != nil {
				fmt.Fprintf(os.Stderr, "ingest %s: %v\n", url, err)
				continue
			}
			changed++
		}
	}
	for url := range remote {
		if seen[url] {
			continue
		}
		// We need the document ID for DeleteDocument. Fetch by URL via ListDocuments
		// or add a GetDocumentByURL RPC. For now, use the existing GetDocuments
		// by ID after listing all docs. Simpler path: extend ListWikiDocuments
		// to include IDs (already in storage, just expose in proto). See note below.
		fmt.Fprintf(os.Stderr, "WARNING: stale remote doc %s — delete via 'mykb' admin (TODO: expose ID in ListWikiDocuments)\n", url)
		_ = deleted
	}

	fmt.Printf("sync: +%d ~%d -%d (vault has %d files, remote had %d)\n",
		added, changed, deleted, len(locals), len(remote))
}

func callIngest(client mykbv1connect.KBServiceClient, url, body, hash string) error {
	_, err := client.IngestMarkdown(context.Background(), connect.NewRequest(&mykbv1.IngestMarkdownRequest{
		Url:         url,
		Body:        body,
		ContentHash: hash,
	}))
	return err
}
```

- [ ] **Step 2: Address the delete TODO by extending the proto with the document ID**

The current `WikiDocument` message has only `url` and `content_hash`. To support clean deletes, add `id`:

Edit `proto/mykb/v1/kb.proto`:

```proto
message WikiDocument {
  string id = 1;
  string url = 2;
  string content_hash = 3;
}
```

Renumber as needed (this is a pre-1.0 internal API; renumbering is safe). Run `just proto`.

Edit `internal/server/server.go` `ListWikiDocuments` handler to populate `Id`. Edit `cmd/mykb/wiki.go` sync flow to call `DeleteDocument` for stale remotes:

```go
		for url, info := range remote {
			if seen[url] {
				continue
			}
			if _, err := client.DeleteDocument(context.Background(), connect.NewRequest(&mykbv1.DeleteDocumentRequest{Id: info.id})); err != nil {
				fmt.Fprintf(os.Stderr, "delete %s: %v\n", url, err)
				continue
			}
			deleted++
		}
```

(Adjust the `remote` map type to hold `(id, hash)`.)

- [ ] **Step 3: Build and smoke test**

```bash
just cli
cd /tmp && rm -rf wikitest && mkdir wikitest && cd wikitest
~/AleCode/mykb/mykb wiki init --name synctest
echo "---
type: concept
date_updated: 2026-04-30
---
# A
" > concepts/a.md
~/AleCode/mykb/mykb wiki sync
# Expected: +1 ~0 -0

echo "edit" >> concepts/a.md
~/AleCode/mykb/mykb wiki sync
# Expected: +0 ~1 -0

rm concepts/a.md
~/AleCode/mykb/mykb wiki sync
# Expected: +0 ~0 -1

cd ~ && rm -rf /tmp/wikitest
```

- [ ] **Step 4: Commit**

```bash
git add cmd/mykb/wiki.go proto/mykb/v1/kb.proto gen/mykb/v1/ internal/server/server.go
git commit -m "feat: implement mykb wiki sync with three-way diff"
```

---

## Task 15: `mykb wiki list`

**Files:**
- Modify: `cmd/mykb/wiki.go`

- [ ] **Step 1: Replace the stub**

```go
func runWikiList(args []string) {
	fs := flag.NewFlagSet("wiki list", flag.ExitOnError)
	vaultOverride := fs.String("vault", "", "vault root (default: auto-discover)")
	fs.Parse(args)

	vaultRoot, err := resolveVault(*vaultOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	type entry struct {
		path, ptype, title string
	}
	var entries []entry
	err = filepath.WalkDir(vaultRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != vaultRoot && (strings.HasPrefix(name, ".") || name == ".templates") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(vaultRoot, path)
		base := filepath.Base(rel)
		if base == "Log.md" || base == "CLAUDE.md" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmStr, content := wiki.SplitFrontmatter(string(body))
		fm, _ := wiki.ParseFrontmatter(fmStr)
		ptype, _ := fm["type"].(string)
		if ptype == "" {
			ptype = "(no type)"
		}
		title := wiki.ExtractTitle(content, base)
		entries = append(entries, entry{
			path: filepath.ToSlash(rel), ptype: ptype, title: title,
		})
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	for _, e := range entries {
		fmt.Printf("%-12s %-30s %s\n", e.ptype, e.path, e.title)
	}
	fmt.Printf("\n%d pages\n", len(entries))
}
```

Add imports: `"sort"`, `"io/fs"`.

- [ ] **Step 2: Build and smoke test**

```bash
just cli
cd /tmp && rm -rf wikitest && mkdir wikitest && cd wikitest
~/AleCode/mykb/mykb wiki init --name listtest
echo "---
type: concept
date_updated: 2026-04-30
---
# A concept" > concepts/a.md
echo "---
type: entity
kind: tool
date_updated: 2026-04-30
---
# Bee tool" > entities/bee.md
~/AleCode/mykb/mykb wiki list
cd ~ && rm -rf /tmp/wikitest
```

Expected:

```
concept      concepts/a.md                  A concept
entity       entities/bee.md                Bee tool

2 pages
```

- [ ] **Step 3: Commit**

```bash
git add cmd/mykb/wiki.go
git commit -m "feat: implement mykb wiki list"
```

---

## Task 16: `mykb wiki lint`

**Files:**
- Modify: `cmd/mykb/wiki.go`

- [ ] **Step 1: Replace the stub**

```go
func runWikiLint(args []string) {
	fs := flag.NewFlagSet("wiki lint", flag.ExitOnError)
	vaultOverride := fs.String("vault", "", "vault root (default: auto-discover)")
	fs.Parse(args)

	vaultRoot, err := resolveVault(*vaultOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	report, err := wiki.Lint(vaultRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint: %v\n", err)
		os.Exit(2)
	}
	for _, e := range report.Errors {
		fmt.Printf("ERROR %s: %s\n", e.Path, e.Message)
	}
	for _, w := range report.Warnings {
		fmt.Printf("WARN  %s: %s\n", w.Path, w.Message)
	}
	fmt.Printf("\n%d errors, %d warnings\n", len(report.Errors), len(report.Warnings))
	if len(report.Errors) > 0 {
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Build and smoke test**

```bash
just cli
cd /tmp && rm -rf wikitest && mkdir wikitest && cd wikitest
~/AleCode/mykb/mykb wiki init --name linttest
echo "# Untyped page" > concepts/no-fm.md
~/AleCode/mykb/mykb wiki lint
echo "exit: $?"
cd ~ && rm -rf /tmp/wikitest
```

Expected: ERROR for missing-frontmatter on `concepts/no-fm.md`. Exit code 1.

- [ ] **Step 3: Commit**

```bash
git add cmd/mykb/wiki.go
git commit -m "feat: implement mykb wiki lint"
```

---

## Task 17: Healthz extension for wiki ingest path

**Files:**
- Modify: `internal/server/http.go` (the `/healthz/deep` handler)

- [ ] **Step 1: Locate the existing deep healthz**

```bash
grep -n "healthz/deep\|deepHealth" /Users/alepar/AleCode/mykb/internal/server/http.go
```

- [ ] **Step 2: Add a wiki-ingest pass**

After the existing deep-healthz steps (and before the cleanup that removes the test document), add a small block that ingests a `wiki://healthz/test.md` document, queries it, asserts it appears, and deletes it. Use the same `Server` methods exposed today.

Pseudocode (adapt to the actual function signature in `http.go`):

```go
// Wiki ingest smoke pass.
const wikiURL = "wiki://healthz/__deep_test__.md"
const wikiBody = "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# Healthz\n\nDeep health check wiki ingest.\n"
hash := pipeline.ComputeContentHash(wikiBody)
ingestResp, err := s.IngestMarkdown(ctx, connect.NewRequest(&mykbv1.IngestMarkdownRequest{
	Url: wikiURL, Body: wikiBody, ContentHash: hash,
}))
if err != nil {
	return fmt.Errorf("wiki ingest: %w", err)
}
defer s.deleteDocument(ctx, ingestResp.Msg.GetDocumentId())
// Optional: query and assert at least one chunk's URL is wikiURL.
```

The exact wiring depends on the existing handler's structure; add the pass following the same pattern.

- [ ] **Step 3: Smoke test**

```bash
just up
curl http://api.mykb.k3s/healthz/deep   # or http://localhost:9091/healthz/deep
```

Expected: 200 OK with whatever the existing endpoint returns, plus no errors logged for the wiki path.

- [ ] **Step 4: Commit**

```bash
git add internal/server/http.go
git commit -m "feat: add wiki ingest pass to /healthz/deep"
```

---

## Self-review

**Spec coverage check:**

| Spec section | Implemented in |
|---|---|
| URL scheme `wiki://name/path` | Task 2 |
| `documents.content_hash` column | Task 1 |
| Type-blind data model | Tasks 1, 5 (no document_type column added) |
| Vault structure (`entities/`, `concepts/`, `synthesis/`, `Log.md`, `CLAUDE.md`) | Task 12 (scaffold) |
| `mykb-wiki.toml` config | Tasks 4, 12 |
| Frontmatter strip before chunking | Task 6 |
| Filesystem cache skipped for wiki docs | Task 6 (no `fs.WriteFiles` call in `WikiIngestor.Ingest`) |
| `IngestMarkdown` RPC | Tasks 7, 8 |
| `ListWikiDocuments` RPC | Tasks 7, 8 |
| `mykb wiki init` | Task 12 |
| `mykb wiki sync` | Task 14 |
| `mykb wiki ingest <file>` | Task 13 |
| `mykb wiki list` | Task 15 |
| `mykb wiki lint` | Tasks 9, 10, 16 |
| Vault auto-discovery (walk up to find `mykb-wiki.toml`) | Task 4 (`DiscoverVaultRoot`) |
| Cascading delete across all stores | Reuses existing `deleteDocument` (verified during brainstorm) |
| Healthz `/healthz/deep` extension | Task 17 |

All spec sections covered.

**Placeholder scan:** No `TBD`, `TODO`, or "implement later" remains. The one TODO comment in Task 14 (about exposing the document ID in `ListWikiDocuments`) is fixed in the same task's Step 2.

**Type consistency:** `WikiDocument` proto has `id, url, content_hash` (after Task 14 Step 2). `Document` storage struct gains `ContentHash` (Task 5). `WikiIngestResult` is consistent across pipeline and server (Tasks 6, 8). `Wikilink` struct is reused in lint (Task 10).

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-30-llm-wiki-on-mykb-plan.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
