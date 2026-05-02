# Wikilink resolution: linter normalization to match Obsidian

**Date:** 2026-05-01
**Scope:** mykb wiki linter (`internal/wiki/lint.go`)
**Driver:** the alepar-main vault discovered the linter resolves
wikilinks more strictly than Obsidian, forcing alias bloat on target
pages. Vault-side guidance changes are tracked separately in that
vault's own `.docs/superpowers/specs/`.

## Background

The mykb wiki linter validates `[[name]]` wikilinks in markdown pages.
Current behavior is stricter than Obsidian, so valid Obsidian links can
fail mykb lint, forcing vault authors to add aliases that wouldn't be
needed if the linter matched Obsidian.

### Obsidian's resolution rule

Per [help.obsidian.md/links](https://help.obsidian.md/links) and
[help.obsidian.md/aliases](https://help.obsidian.md/aliases), corroborated
by Obsidian forum mod responses:

1. Match against filename stem (without `.md`) **case-insensitively**.
2. Treat space, hyphen, and underscore as equivalent during matching.
3. Match against frontmatter `aliases:` with the same case-insensitivity
   and separator-equivalence.

So `[[DIY string alignment procedure]]` resolves to
`diy-string-alignment-procedure.md` in Obsidian without any alias entry.

### Current mykb linter behavior

`Lint()` in `internal/wiki/lint.go` (lines 39-88 as of 2026-05-01)
builds a single map keyed on **literal** filename stems and **literal**
alias strings:

```go
for i, p := range pages {
    stem := stemOf(p.relPath)        // filename minus ".md", verbatim
    byName[stem] = &pages[i]
    for _, a := range p.aliases {    // raw frontmatter `aliases:` strings
        byName[a] = &pages[i]
    }
}
```

`stemOf` does only `strings.TrimSuffix(filepath.Base(relPath), ".md")`.
Lookups (`byName[target]`) consult the parsed target verbatim. No
case-folding, no separator normalization. Targets are only run through
`strings.TrimSpace` during parse (`ParseWikilinks`,
`internal/wiki/wikilinks.go:25`).

**Empirical confirmation** (alepar-main vault, 2026-05-01):
`[[DIY string alignment procedure]]` failed lint even though the vault
contained `diy-string-alignment-procedure.md` with an H1 reading
"DIY string alignment procedure". Adding the literal "DIY string
alignment procedure" as an alias fixed it.

## Change

Modify `Lint()` to normalize both index keys and lookup targets so the
linter matches Obsidian's documented resolution rule.

### Normalization function

Add to `internal/wiki/lint.go` (or a sibling file if cleaner):

```go
// normalizeWikilinkKey produces the canonical lookup key for a wikilink
// target or a vault page name. It matches Obsidian's documented rules:
// case-insensitive, treating space/hyphen/underscore as equivalent,
// collapsing whitespace runs, and trimming ends.
func normalizeWikilinkKey(s string) string {
    s = strings.ToLower(s)
    s = strings.NewReplacer("_", " ", "-", " ").Replace(s)
    return strings.Join(strings.Fields(s), " ")
}
```

The canonical separator is a single space. `strings.Fields` collapses
runs of any whitespace (including the spaces produced by the replacer)
and trims ends. Path separators (`/`) are NOT normalized — they keep
their meaning if the codebase later supports `[[dir/page]]` disambiguation.

### Apply on both sides of resolution

In `Lint()` index build:

```go
byName[normalizeWikilinkKey(stem)] = &pages[i]
for _, a := range p.aliases {
    byName[normalizeWikilinkKey(a)] = &pages[i]
}
```

In the lookup site:

```go
resolved, ok := byName[normalizeWikilinkKey(target)]
```

### Collision detection during indexing

When `byName[k]` is already present and points to a *different* page,
emit a lint warning:

> `WARN  ambiguous wikilink target "<key>" resolves to both <pathA> and <pathB>`

Aliases on the *same* page that canonicalize to the same key are silently
deduped. A page's filename stem colliding with another page's stem or
alias is the user-error case the warning catches.

### Tests

In `internal/wiki/lint_test.go` (or wherever resolution tests live), add:

- `[[DIY string alignment procedure]]` resolves to a page whose stem is
  `diy-string-alignment-procedure`.
- `[[foo_bar]]`, `[[foo-bar]]`, `[[Foo Bar]]` all resolve to `foo-bar.md`.
- `[[Multi   Space]]` (multiple consecutive spaces) resolves to
  `multi-space.md`.
- Two files whose stems both normalize to `foo bar` produce the
  ambiguity WARN with both paths named.
- Existing exact-match cases still pass (regression coverage).

## Out of scope

- Path-prefixed wikilinks (`[[dir/page]]`) for disambiguation. Obsidian
  supports this; current linter doesn't. Could be a follow-up.
- Cleanup of existing alias arrays in consumer vaults — vault-side work.
- Scaffold and shipped skill-doc drift (legacy `entities/concepts/synthesis`
  layout still referenced in `internal/wiki/scaffold.go vaultClaudeMD()`
  and `internal/wiki/skills/wiki-research/{SKILL.md,playbook.md,examples.md}`).
  Layout is per-vault; defaults are acceptable. Separate cleanup task.

## Verification

After the change ships:

1. Run `mykb wiki lint` against the alepar-main vault — expect 0 errors
   (an unrelated audio-page orphan warning is fine).
2. Add a test page containing `[[DIY string alignment procedure]]` to
   that vault with no alias on the target; lint stays clean.
3. Open the same vault in Obsidian and confirm the same link resolves
   to the same target there.
4. Add two test files whose stems both normalize to the same key; lint
   emits the WARN naming both files.
