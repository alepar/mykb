//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWikiLifecycle drives the full wiki workflow: scaffold a vault, sync
// pages, query them, edit/delete, sync again, and verify the search index
// reflects each change.
func TestWikiLifecycle(t *testing.T) {
	vault := tempVault(t, "e2e")

	// Step 1: write three valid wiki pages.
	writeFile(t, vault, "entities/sandwich.md", `---
type: entity
kind: product
date_updated: 2026-04-30
---

# Sandwich

A `+"`sandwich`"+` is a food made of bread and filling. The Earl of Sandwich
allegedly invented it in 1762 to eat at the gaming table.
`)

	writeFile(t, vault, "concepts/triangulation.md", `---
type: concept
date_updated: 2026-04-30
---

# Triangulation

Triangulation is a measurement technique using three reference points to
locate something precisely. Used in surveying, navigation, and crystallography.
`)

	writeFile(t, vault, "synthesis/why-bread.md", `---
type: synthesis
question: "Why is bread important to civilizations?"
answered_at: 2026-04-30
date_updated: 2026-04-30
superseded_by: null
---

# Why is bread important?

Bread is a staple grain food across most civilizations. Wheat cultivation
predates writing. The combination of yeast and grain made caloric storage
possible at scale.
`)

	// Step 2: initial sync should add all three.
	stdout, stderr, code := runMykb(t, vault, "wiki", "sync")
	if code != 0 {
		t.Fatalf("wiki sync failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "+3") {
		t.Errorf("expected '+3' in initial sync output, got: %s", stdout)
	}

	// Step 3: confirm each wiki URL is in ListDocuments.
	for _, url := range []string{
		"wiki://e2e/entities/sandwich.md",
		"wiki://e2e/concepts/triangulation.md",
		"wiki://e2e/synthesis/why-bread.md",
	} {
		if findDocumentByURL(t, url) == nil {
			t.Errorf("after sync, document %q not in ListDocuments", url)
		}
	}

	// Step 4: query for each page's distinctive content.
	waitFor(t, 5*time.Second, func() bool {
		return queryAPIHasURL(t, "Earl of Sandwich gaming table", "wiki://e2e/entities/sandwich.md")
	}, "sandwich entity should be searchable")

	waitFor(t, 5*time.Second, func() bool {
		return queryAPIHasURL(t, "triangulation surveying navigation", "wiki://e2e/concepts/triangulation.md")
	}, "triangulation concept should be searchable")

	waitFor(t, 5*time.Second, func() bool {
		return queryAPIHasURL(t, "wheat cultivation civilizations bread", "wiki://e2e/synthesis/why-bread.md")
	}, "why-bread synthesis should be searchable")

	// Step 5: edit a page and re-sync — should report ~1.
	writeFile(t, vault, "entities/sandwich.md", `---
type: entity
kind: product
date_updated: 2026-04-30
---

# Sandwich

A sandwich is a food made of bread and filling. The Earl of Sandwich
allegedly invented it in 1762. Modern variants include the croque-monsieur,
banh mi, and the Reuben.
`)
	stdout, _, code = runMykb(t, vault, "wiki", "sync")
	if code != 0 {
		t.Fatalf("wiki sync (after edit) failed: exit %d\nstdout: %s", code, stdout)
	}
	if !strings.Contains(stdout, "~1") {
		t.Errorf("expected '~1' in edit sync output, got: %s", stdout)
	}

	// Step 6: query for new content; old content may or may not still match.
	waitFor(t, 5*time.Second, func() bool {
		return queryAPIHasURL(t, "croque monsieur banh mi Reuben", "wiki://e2e/entities/sandwich.md")
	}, "edited sandwich content should be searchable")

	// Step 7: idempotent re-sync — no changes.
	stdout, _, code = runMykb(t, vault, "wiki", "sync")
	if code != 0 {
		t.Fatalf("wiki sync (idempotent) failed: exit %d\nstdout: %s", code, stdout)
	}
	if !strings.Contains(stdout, "+0 ~0 -0") {
		t.Errorf("expected '+0 ~0 -0' in idempotent sync output, got: %s", stdout)
	}

	// Step 8: delete a file, sync — should report -1, doc gone from search.
	deletePath := filepath.Join(vault, "concepts/triangulation.md")
	if err := os.Remove(deletePath); err != nil {
		t.Fatalf("delete vault file: %v", err)
	}
	stdout, _, code = runMykb(t, vault, "wiki", "sync")
	if code != 0 {
		t.Fatalf("wiki sync (after delete) failed: exit %d\nstdout: %s", code, stdout)
	}
	if !strings.Contains(stdout, "-1") {
		t.Errorf("expected '-1' in delete sync output, got: %s", stdout)
	}

	if findDocumentByURL(t, "wiki://e2e/concepts/triangulation.md") != nil {
		t.Errorf("after sync delete, triangulation doc still in ListDocuments")
	}

	waitFor(t, 5*time.Second, func() bool {
		return !queryAPIHasURL(t, "triangulation surveying navigation", "wiki://e2e/concepts/triangulation.md")
	}, "deleted triangulation should disappear from search")

	// Step 9: `mykb wiki list` should show exactly the remaining 2 pages.
	stdout, _, code = runMykb(t, vault, "wiki", "list")
	if code != 0 {
		t.Fatalf("wiki list failed: exit %d\nstdout: %s", code, stdout)
	}
	if !strings.Contains(stdout, "entities/sandwich.md") {
		t.Errorf("wiki list missing sandwich: %s", stdout)
	}
	if !strings.Contains(stdout, "synthesis/why-bread.md") {
		t.Errorf("wiki list missing why-bread: %s", stdout)
	}
	if strings.Contains(stdout, "triangulation.md") {
		t.Errorf("wiki list still includes deleted triangulation: %s", stdout)
	}
	if !strings.Contains(stdout, "2 pages") {
		t.Errorf("wiki list footer should say '2 pages', got: %s", stdout)
	}

	// Step 10: lint should pass (no errors); orphan warns are OK because
	// these pages don't link to each other.
	stdout, _, code = runMykb(t, vault, "wiki", "lint")
	if code != 0 {
		t.Errorf("wiki lint on a clean vault should exit 0, got %d\nstdout: %s", code, stdout)
	}
}

// TestWikiLintErrors verifies that lint catches real errors and exits non-zero.
func TestWikiLintErrors(t *testing.T) {
	vault := tempVault(t, "e2elint")

	// Page with no frontmatter at all.
	writeFile(t, vault, "concepts/no-fm.md", "# A page with no frontmatter\n\nbody")

	// Page with a broken short-form wikilink.
	writeFile(t, vault, "concepts/broken.md", `---
type: concept
date_updated: 2026-04-30
---

# Broken

See [[nonexistent-target-page]] for details.
`)

	stdout, _, code := runMykb(t, vault, "wiki", "lint")
	if code == 0 {
		t.Errorf("wiki lint should exit non-zero with errors, got 0\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "missing required frontmatter") {
		t.Errorf("expected 'missing required frontmatter' in lint output: %s", stdout)
	}
	if !strings.Contains(stdout, "broken wikilink") {
		t.Errorf("expected 'broken wikilink' in lint output: %s", stdout)
	}
}

