package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	ioFs "io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"connectrpc.com/connect"

	mykbv1 "mykb/gen/mykb/v1"
	"mykb/gen/mykb/v1/mykbv1connect"
	"mykb/internal/cliconfig"
	"mykb/internal/wiki"
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
	fmt.Fprintln(os.Stderr, "  mykb wiki init [--vault DIR]                              scaffold a new wiki vault")
	fmt.Fprintln(os.Stderr, "  mykb wiki sync [--vault DIR] [--host HOST] [--force]      sync vault with mykb (diff-based; --force re-ingests every file)")
	fmt.Fprintln(os.Stderr, "  mykb wiki ingest <file> [--vault DIR] [--host HOST] [--force]   ingest a single file (--force bypasses content_hash idempotency)")
	fmt.Fprintln(os.Stderr, "  mykb wiki list [--vault DIR]                              list vault inventory")
	fmt.Fprintln(os.Stderr, "  mykb wiki lint [--vault DIR]                              validate vault structure")
}

// Stub implementations — replaced one by one in Tasks 12-16.

func runWikiInit(args []string) {
	fs := flag.NewFlagSet("wiki init", flag.ExitOnError)
	dir := fs.String("vault", ".", "directory to scaffold the vault in")
	name := fs.String("name", "", "wiki name (will appear in URL prefix wiki://<name>/...)")
	skipSubmodule := fs.Bool("no-submodule", false, "skip registering the deep-research git submodule (offline init)")
	fs.Parse(args) //nolint:errcheck

	wikiName := *name
	if wikiName == "" {
		// On re-run, prefer the name from the existing config rather than re-prompting.
		if cfg, err := wiki.LoadVaultConfig(*dir); err == nil {
			wikiName = cfg.Name
		}
	}
	if wikiName == "" {
		fmt.Fprint(os.Stderr, "Wiki name: ")
		var s string
		_, _ = fmt.Fscanln(os.Stdin, &s)
		wikiName = strings.TrimSpace(s)
	}
	if wikiName == "" {
		fmt.Fprintln(os.Stderr, "wiki name is required")
		os.Exit(1)
	}

	result, err := wiki.ScaffoldVault(*dir, wikiName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scaffold failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("scaffolded wiki %q in %s: %d files written, %d already present\n",
		wikiName, *dir, len(result.Written), len(result.Skipped))

	if *skipSubmodule {
		fmt.Println("skipping deep-research submodule (--no-submodule)")
		return
	}

	if _, err := ensureGitRepo(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not ensure git repo: %v\n", err)
		fmt.Fprintln(os.Stderr, "skipping submodule registration; init the repo and re-run `mykb wiki init` to add it")
		return
	}
	if err := ensureDeepResearchSubmodule(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not register deep-research submodule: %v\n", err)
		fmt.Fprintln(os.Stderr, "the wiki-research skill will not be able to invoke deep-research until this is resolved")
	}
}

// ensureGitRepo runs `git init` in dir if it is not already a git repo.
// Returns true if a new repo was initialized.
func ensureGitRepo(dir string) (bool, error) {
	gitPath := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitPath); err == nil {
		return false, nil
	}
	cmd := exec.Command("git", "-C", dir, "init")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("git init: %w", err)
	}
	return true, nil
}

// ensureDeepResearchSubmodule registers the upstream claude-deep-research-skill
// repo as a git submodule at .claude/skills/deep-research, if not already
// registered. Idempotent: a no-op when the submodule path is already in
// .gitmodules.
func ensureDeepResearchSubmodule(dir string) error {
	if registered, err := submoduleRegistered(dir, wiki.DeepResearchSubmodulePath); err != nil {
		return err
	} else if registered {
		fmt.Printf("deep-research submodule already registered at %s\n", wiki.DeepResearchSubmodulePath)
		return nil
	}
	fmt.Printf("registering deep-research submodule at %s ...\n", wiki.DeepResearchSubmodulePath)
	cmd := exec.Command("git", "-C", dir, "submodule", "add",
		wiki.DeepResearchSubmoduleURL, wiki.DeepResearchSubmodulePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git submodule add: %w", err)
	}
	return nil
}

// submoduleRegistered reports whether the given vault-relative path appears as
// a registered submodule in <dir>/.gitmodules.
func submoduleRegistered(dir, submodulePath string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".gitmodules"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Match a `path = <submodulePath>` line; this is what `git submodule add` writes.
	needle := "path = " + submodulePath
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == needle {
			return true, nil
		}
	}
	return false, nil
}

func runWikiSync(args []string) {
	fs := flag.NewFlagSet("wiki sync", flag.ExitOnError)
	vaultOverride := fs.String("vault", "", "vault root (default: auto-discover)")
	host := fs.String("host", "", "server address (default: from config)")
	force := fs.Bool("force", false, "re-ingest every local file regardless of content_hash match")
	fs.Parse(args) //nolint:errcheck

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

	// Walk vault, computing content hashes.
	type local struct {
		relPath string
		hash    string
	}
	var locals []local
	err = filepath.WalkDir(vaultRoot, func(path string, d ioFs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != vaultRoot && (strings.HasPrefix(name, ".") || name == ".templates") {
				return ioFs.SkipDir
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
	type remoteEntry struct {
		id   string
		hash string
	}
	remote := map[string]remoteEntry{}
	for _, d := range listResp.Msg.GetDocuments() {
		remote[d.GetUrl()] = remoteEntry{id: d.GetId(), hash: d.GetContentHash()}
	}

	// Three-way diff. With --force, every local file is treated as changed.
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
		existing, ok := remote[url]
		switch {
		case !ok:
			if err := callIngest(client, url, string(body), l.hash, *force); err != nil {
				fmt.Fprintf(os.Stderr, "ingest %s: %v\n", url, err)
				continue
			}
			added++
		case *force || existing.hash != l.hash:
			if err := callIngest(client, url, string(body), l.hash, *force); err != nil {
				fmt.Fprintf(os.Stderr, "ingest %s: %v\n", url, err)
				continue
			}
			changed++
		}
	}
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

	mode := ""
	if *force {
		mode = " (force)"
	}
	fmt.Printf("sync%s: +%d ~%d -%d (vault has %d files, remote had %d)\n",
		mode, added, changed, deleted, len(locals), len(remote))
}

func callIngest(client mykbv1connect.KBServiceClient, url, body, hash string, force bool) error {
	_, err := client.IngestMarkdown(context.Background(), connect.NewRequest(&mykbv1.IngestMarkdownRequest{
		Url:         url,
		Body:        body,
		ContentHash: hash,
		Force:       force,
	}))
	return err
}

func runWikiIngest(args []string) {
	fs := flag.NewFlagSet("wiki ingest", flag.ExitOnError)
	vaultOverride := fs.String("vault", "", "vault root (default: auto-discover)")
	host := fs.String("host", "", "server address (default: from config)")
	force := fs.Bool("force", false, "bypass content_hash idempotency and re-ingest unconditionally")
	fs.Parse(args) //nolint:errcheck

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: mykb wiki ingest <file> [--vault DIR] [--host HOST] [--force]")
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
		Force:       *force,
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

func runWikiList(args []string) {
	flagSet := flag.NewFlagSet("wiki list", flag.ExitOnError)
	vaultOverride := flagSet.String("vault", "", "vault root (default: auto-discover)")
	flagSet.Parse(args) //nolint:errcheck

	vaultRoot, err := resolveVault(*vaultOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	type entry struct {
		path, ptype, title string
	}
	var entries []entry
	err = filepath.WalkDir(vaultRoot, func(path string, d ioFs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != vaultRoot && (strings.HasPrefix(name, ".") || name == ".templates") {
				return ioFs.SkipDir
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

func runWikiLint(args []string) {
	flagSet := flag.NewFlagSet("wiki lint", flag.ExitOnError)
	vaultOverride := flagSet.String("vault", "", "vault root (default: auto-discover)")
	flagSet.Parse(args) //nolint:errcheck

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

// pipelineComputeContentHash mirrors pipeline.ComputeContentHash without the dep.
// (CLI shouldn't import server-side packages like internal/pipeline.)
func pipelineComputeContentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
