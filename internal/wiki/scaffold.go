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
//
// Returns an error if any required file already exists.
func ScaffoldVault(dir, wikiName string) error {
	if wikiName == "" {
		return fmt.Errorf("wiki name is empty")
	}
	if !wikiNameRegexp.MatchString(wikiName) {
		return fmt.Errorf("wiki name %q is invalid (must match [a-zA-Z0-9_-]+)", wikiName)
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
