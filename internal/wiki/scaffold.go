package wiki

import (
	"embed"
	"fmt"
	ioFs "io/fs"
	"os"
	"path/filepath"
	"sort"
)

//go:embed skills/wiki-research/*
var embeddedSkills embed.FS

// DeepResearchSubmoduleURL is the upstream git URL for the claude-deep-research-skill,
// which mykb wiki vaults install as a submodule under .claude/skills/deep-research.
const DeepResearchSubmoduleURL = "https://github.com/199-biotechnologies/claude-deep-research-skill.git"

// DeepResearchSubmodulePath is the vault-relative path where the deep-research
// skill is registered as a git submodule.
const DeepResearchSubmodulePath = ".claude/skills/deep-research"

// ScaffoldResult reports what ScaffoldVault did. Both lists are vault-relative,
// in slash-separated form, sorted for deterministic output.
type ScaffoldResult struct {
	Written []string // files created on this run
	Skipped []string // files that already existed and were preserved
}

// ScaffoldVault writes the initial vault layout into `dir`. It creates:
//   - mykb-wiki.toml (with the given wiki name)
//   - CLAUDE.md (the wiki operating manual)
//   - Log.md (with header)
//   - entities/, concepts/, synthesis/ (with .gitkeep)
//   - .templates/{entity,concept,synthesis}.md
//   - .claude/skills/wiki-research/{SKILL.md,playbook.md,examples.md}
//
// Idempotent: existing files are preserved byte-for-byte; only missing files
// are written. Re-running on an already-scaffolded vault is a no-op success.
//
// Note: ScaffoldVault does NOT register the deep-research git submodule; that
// step shells out to `git` and is the caller's responsibility (see cmd/mykb/wiki.go).
func ScaffoldVault(dir, wikiName string) (*ScaffoldResult, error) {
	if wikiName == "" {
		return nil, fmt.Errorf("wiki name is empty")
	}
	if !wikiNameRegexp.MatchString(wikiName) {
		return nil, fmt.Errorf("wiki name %q is invalid (must match [a-zA-Z0-9_-]+)", wikiName)
	}

	files := map[string]string{
		"mykb-wiki.toml":          fmt.Sprintf("name = %q\nstale_after_days = 180\n", wikiName),
		"CLAUDE.md":               vaultClaudeMD(wikiName),
		"Log.md":                  "# Log\n\n",
		"entities/.gitkeep":       "",
		"concepts/.gitkeep":       "",
		"synthesis/.gitkeep":      "",
		".templates/entity.md":    templateEntity,
		".templates/concept.md":   templateConcept,
		".templates/synthesis.md": templateSynthesis,
	}

	skillFiles, err := embeddedSkillFiles()
	if err != nil {
		return nil, fmt.Errorf("load embedded skill files: %w", err)
	}
	for path, content := range skillFiles {
		files[path] = content
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	result := &ScaffoldResult{}
	for _, path := range paths {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); err == nil {
			result.Skipped = append(result.Skipped, path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, []byte(files[path]), 0o644); err != nil {
			return nil, err
		}
		result.Written = append(result.Written, path)
	}
	return result, nil
}

// embeddedSkillFiles returns a map of vault-relative path -> file content for
// every file shipped under the embedded skills/ tree. Paths are rewritten to
// land under .claude/skills/ in the target vault.
func embeddedSkillFiles() (map[string]string, error) {
	out := map[string]string{}
	err := ioFs.WalkDir(embeddedSkills, "skills", func(path string, d ioFs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := embeddedSkills.ReadFile(path)
		if err != nil {
			return err
		}
		// "skills/wiki-research/SKILL.md" -> ".claude/skills/wiki-research/SKILL.md"
		out[filepath.ToSlash(filepath.Join(".claude", path))] = string(data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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

## Skills
Two Claude skills ship inside the vault under .claude/skills/:

- **wiki-research** (in .claude/skills/wiki-research/) — disciplined research loop:
  search mykb first, optionally invoke deep-research with mykb context as a
  seed, cross-check, write a synthesis page. Use it for any non-trivial
  research request. Embedded in the mykb binary; updates with new mykb releases.

- **deep-research** (in .claude/skills/deep-research/) — pinned as a git submodule
  pointing at https://github.com/199-biotechnologies/claude-deep-research-skill.
  Drives multi-source web research with citation tracking. The wiki-research
  skill calls into it as needed; you generally won't invoke it directly.

## Keeping skills up to date
The deep-research skill is a git submodule, so its version is pinned in this
vault's commit history. To populate, update, or roll back:

    # After cloning the vault, populate the deep-research submodule:
    git submodule update --init --recursive

    # Periodically (e.g., monthly) check for upstream updates:
    git submodule update --remote .claude/skills/deep-research
    git -C .claude/skills/deep-research log --oneline -10   # review what changed
    git add .claude/skills/deep-research && git commit -m "bump deep-research skill"

    # Roll back to a prior commit if an update breaks things:
    cd .claude/skills/deep-research && git checkout <prior-sha> && cd -
    git add .claude/skills/deep-research && git commit -m "pin deep-research to <prior-sha>"

To update wiki-research itself, upgrade mykb (e.g., 'just cli' from the mykb
repo) and re-run 'mykb wiki init' here — re-running is idempotent and only
writes missing or new scaffold files.
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
