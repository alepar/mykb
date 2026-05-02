# Wikilink Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `mykb wiki lint` resolve `[[name]]` wikilinks the way Obsidian does — case-insensitive, with space/hyphen/underscore treated as equivalent, plus a collision warning when two pages canonicalize to the same key.

**Architecture:** Add a single `normalizeWikilinkKey(s string) string` helper in `internal/wiki/lint.go`. Apply it to every key written into the in-memory `byName` map (filename stems + frontmatter aliases) and to every lookup target. Detect collisions while building the index and emit a `WARN` finding. No proto, server, CLI, or storage changes — this is pure linter behavior.

**Tech Stack:** Go (stdlib `strings`), existing `internal/wiki` test fixtures.

**Spec:** `docs/superpowers/specs/2026-05-01-wikilink-resolution-design.md`

---

## File Structure

- Modify: `internal/wiki/lint.go`
  - Add `normalizeWikilinkKey` near `stemOf` (bottom of file).
  - Update `byName` index build (lines 39-48) to use the normalizer and to detect collisions.
  - Update wikilink resolution lookup (line 79) to use the normalizer.
- Modify: `internal/wiki/lint_test.go`
  - Add table-driven test for `normalizeWikilinkKey`.
  - Add test cases for case-insensitive / separator-equivalent resolution.
  - Add test for the collision WARN.

No new files. The helper lives in `lint.go` because it has a single caller (the linter) and grouping it with the resolution logic keeps it discoverable. If a future change adds another caller, promote it to its own file then.

---

## Task 1: Add and test `normalizeWikilinkKey`

**Files:**
- Modify: `internal/wiki/lint.go` (add helper near `stemOf`)
- Modify: `internal/wiki/lint_test.go` (add table-driven test)

- [ ] **Step 1: Write the failing test**

Add to `internal/wiki/lint_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/wiki/ -run TestNormalizeWikilinkKey -v`
Expected: FAIL with `undefined: normalizeWikilinkKey`.

- [ ] **Step 3: Implement `normalizeWikilinkKey`**

Add to `internal/wiki/lint.go`, immediately above `stemOf`:

```go
// normalizeWikilinkKey produces the canonical lookup key for a wikilink
// target or a vault page name. It matches Obsidian's documented rules:
// case-insensitive, treating space/hyphen/underscore as equivalent,
// collapsing whitespace runs, and trimming ends. Path separators (`/`)
// are preserved so future path-prefixed wikilinks can disambiguate.
func normalizeWikilinkKey(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("_", " ", "-", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/wiki/ -run TestNormalizeWikilinkKey -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/lint.go internal/wiki/lint_test.go
git commit -m "wiki: add normalizeWikilinkKey helper for Obsidian-compatible link resolution"
```

---

## Task 2: Apply normalization to lint index and lookup

**Files:**
- Modify: `internal/wiki/lint.go` (lines 39-48 index build, line 79 lookup)
- Modify: `internal/wiki/lint_test.go` (add resolution tests)

- [ ] **Step 1: Write failing tests for case-insensitive / separator-equivalent resolution**

Add to `internal/wiki/lint_test.go`:

```go
func TestLintWikilinkResolutionCaseInsensitive(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml": `name = "main"`,
		"concepts/diy-string-alignment-procedure.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# DIY string alignment procedure\n",
		"concepts/caller.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# Caller\nSee [[DIY string alignment procedure]].",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range report.Errors {
		if strings.Contains(e.Path, "caller.md") && strings.Contains(e.Message, "broken wikilink") {
			t.Errorf("did not expect broken-wikilink error, got: %+v", e)
		}
	}
}

func TestLintWikilinkResolutionSeparatorEquivalence(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml": `name = "main"`,
		"concepts/foo-bar.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# Foo Bar\n",
		"concepts/a.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# A\nSee [[foo_bar]] and [[Foo Bar]] and [[foo-bar]].",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range report.Errors {
		if strings.Contains(e.Path, "a.md") && strings.Contains(e.Message, "broken wikilink") {
			t.Errorf("did not expect broken-wikilink error, got: %+v", e)
		}
	}
}

func TestLintWikilinkResolutionWhitespaceCollapse(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml": `name = "main"`,
		"concepts/multi-space.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# Multi space\n",
		"concepts/a.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# A\nSee [[Multi   Space]].",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range report.Errors {
		if strings.Contains(e.Path, "a.md") && strings.Contains(e.Message, "broken wikilink") {
			t.Errorf("did not expect broken-wikilink error, got: %+v", e)
		}
	}
}

func TestLintWikilinkResolutionAliasNormalized(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml": `name = "main"`,
		"entities/widget.md": "---\ntype: entity\nkind: tool\ndate_updated: 2026-04-30\naliases:\n  - The Widget System\n---\n# Widget\n",
		"concepts/a.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# A\nSee [[the-widget-system]] and [[the_widget_system]].",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range report.Errors {
		if strings.Contains(e.Path, "a.md") && strings.Contains(e.Message, "broken wikilink") {
			t.Errorf("did not expect broken-wikilink error, got: %+v", e)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/wiki/ -run 'TestLintWikilinkResolution' -v`
Expected: All four FAIL with `broken wikilink` errors (current literal-match index doesn't normalize).

- [ ] **Step 3: Update the index build and lookup to normalize**

In `internal/wiki/lint.go`, replace lines 39-48 (the `byName` build) with:

```go
	// Build name -> page map for short-form wikilink resolution.
	// Keys are normalized (Obsidian-style: case-insensitive, space/-/_ equivalent).
	byName := map[string]*vaultPage{}
	for i, p := range pages {
		key := normalizeWikilinkKey(stemOf(p.relPath))
		byName[key] = &pages[i]
		// Also register frontmatter aliases (entities only).
		for _, a := range p.aliases {
			byName[normalizeWikilinkKey(a)] = &pages[i]
		}
	}
```

In the same file, replace the lookup line (currently `resolved, ok := byName[target]`) with:

```go
			resolved, ok := byName[normalizeWikilinkKey(target)]
```

- [ ] **Step 4: Run the resolution tests to verify they pass**

Run: `go test ./internal/wiki/ -run 'TestLintWikilinkResolution' -v`
Expected: All four PASS.

- [ ] **Step 5: Run the full lint test suite to verify no regressions**

Run: `go test ./internal/wiki/ -v`
Expected: All tests PASS, including pre-existing `TestLintBrokenWikilink`, `TestLintMissingFrontmatter`, `TestLintOrphan`, `TestLintStale`.

- [ ] **Step 6: Commit**

```bash
git add internal/wiki/lint.go internal/wiki/lint_test.go
git commit -m "wiki: normalize wikilink resolution to match Obsidian"
```

---

## Task 3: Add ambiguity warning for colliding keys

**Files:**
- Modify: `internal/wiki/lint.go` (extend index build)
- Modify: `internal/wiki/lint_test.go` (add collision test)

**Design notes for this task:**
- Two distinct *pages* whose stems normalize to the same key → emit one WARN naming both paths.
- An *alias* on page A that collides with page B's stem (or another page's alias) → also a WARN.
- An alias on page A whose normalized key equals A's own normalized stem → silently ignored (the spec calls this "deduped").
- Multiple aliases on the same page that canonicalize to the same key → silently deduped (same page, same key).
- Findings are deterministic across runs: emit warnings in lexicographic order of the colliding key, naming the two paths in lexicographic order.

- [ ] **Step 1: Write the failing test**

Add to `internal/wiki/lint_test.go`:

```go
func TestLintWikilinkAmbiguityWarning(t *testing.T) {
	v := writeFixture(t, map[string]string{
		"mykb-wiki.toml": `name = "main"`,
		"concepts/foo-bar.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# Foo Bar\n",
		"concepts/foo_bar.md": "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# Foo Bar (other)\n",
		"concepts/caller.md":  "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# Caller\nSee [[foo bar]].",
	})
	report, err := Lint(v)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range report.Warnings {
		if strings.Contains(w.Message, "ambiguous wikilink target") &&
			strings.Contains(w.Message, "foo bar") &&
			strings.Contains(w.Message, "concepts/foo-bar.md") &&
			strings.Contains(w.Message, "concepts/foo_bar.md") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ambiguity warning naming both colliding files, got: %+v", report.Warnings)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/wiki/ -run TestLintWikilinkAmbiguityWarning -v`
Expected: FAIL — no ambiguity warning is emitted yet.

- [ ] **Step 3: Implement collision detection in the index build**

In `internal/wiki/lint.go`, replace the index build introduced in Task 2 with:

```go
	// Build name -> page map for short-form wikilink resolution.
	// Keys are normalized (Obsidian-style: case-insensitive, space/-/_ equivalent).
	// When two distinct pages share a normalized key, emit one ambiguity warning
	// per colliding key and keep the first-registered page as the resolution.
	byName := map[string]*vaultPage{}
	type collision struct {
		key, pathA, pathB string
	}
	var collisions []collision
	register := func(key string, page *vaultPage) {
		existing, ok := byName[key]
		if !ok {
			byName[key] = page
			return
		}
		if existing == page {
			return // same page, alias dedup
		}
		a, b := existing.relPath, page.relPath
		if b < a {
			a, b = b, a
		}
		collisions = append(collisions, collision{key: key, pathA: a, pathB: b})
	}
	for i := range pages {
		p := &pages[i]
		register(normalizeWikilinkKey(stemOf(p.relPath)), p)
		for _, a := range p.aliases {
			register(normalizeWikilinkKey(a), p)
		}
	}
	sort.Slice(collisions, func(i, j int) bool { return collisions[i].key < collisions[j].key })
	seenColl := map[string]bool{}
	for _, c := range collisions {
		if seenColl[c.key] {
			continue
		}
		seenColl[c.key] = true
		report.Warnings = append(report.Warnings, LintFinding{
			Path:    c.pathA,
			Message: fmt.Sprintf("ambiguous wikilink target %q resolves to both %s and %s", c.key, c.pathA, c.pathB),
		})
	}
```

Add `"sort"` to the import block at the top of the file.

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/wiki/ -run TestLintWikilinkAmbiguityWarning -v`
Expected: PASS.

- [ ] **Step 5: Run the full wiki test suite**

Run: `go test ./internal/wiki/ -v`
Expected: All tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/wiki/lint.go internal/wiki/lint_test.go
git commit -m "wiki: warn on ambiguous wikilink targets after normalization"
```

---

## Task 4: Repository-wide verification

**Files:** none modified — this task is verification only.

- [ ] **Step 1: Run the whole test suite**

Run: `just test`
Expected: PASS. If anything fails outside `internal/wiki/`, stop and investigate; this change should be self-contained.

- [ ] **Step 2: Run lint**

Run: `just lint`
Expected: clean (or pre-existing warnings only).

- [ ] **Step 3: Build the CLI**

Run: `just cli`
Expected: produces `./mykb` binary with no errors.

- [ ] **Step 4: Smoke-test against the alepar-main vault**

The vault path is configured in the user's environment. Run:

```bash
./mykb wiki lint --vault ~/AleCode/alepar-main
```

Expected outcome (per spec verification step 1): zero errors. Pre-existing orphan warnings about audio pages are fine. If `[[DIY string alignment procedure]]` previously errored, it should now resolve to `diy-string-alignment-procedure.md` without complaint.

If the vault path is different, ask the user before running. If the user prefers to verify themselves, skip this step and report the binary is ready.

---

## Self-Review Notes

**Spec coverage:**
- Normalization function (spec §Normalization function) → Task 1.
- Apply on both sides of resolution (spec §Apply on both sides of resolution) → Task 2.
- Collision detection during indexing (spec §Collision detection during indexing) → Task 3.
- Test cases enumerated in spec §Tests → Tasks 1-3 cover all five (DIY string alignment, foo_bar/foo-bar/Foo Bar, multi-space, ambiguity, regression). Existing exact-match coverage is preserved by `TestLintBrokenWikilink` not being touched.
- Verification §Verification step 1-4 → Task 4.

**Out-of-scope items left out (per spec):** path-prefixed wikilinks, vault-side alias cleanup, scaffold/skill-doc layout drift. None of these appear in the plan.

**Type/signature consistency:** the helper signature `normalizeWikilinkKey(s string) string` is identical in every task that uses it. The `register` closure introduced in Task 3 replaces the inline loop from Task 2 — Task 3's Step 3 shows the full replacement block, so the engineer doesn't need to reconcile partial edits.
