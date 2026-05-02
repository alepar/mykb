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
			resolved, ok := byName[normalizeWikilinkKey(target)]
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

func stemOf(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, ".md")
}

func pathExists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}
