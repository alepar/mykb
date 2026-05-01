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
	"path/filepath"
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
	fmt.Fprintln(os.Stderr, "  mykb wiki init [--vault DIR]                  scaffold a new wiki vault")
	fmt.Fprintln(os.Stderr, "  mykb wiki sync [--vault DIR] [--host HOST]    sync vault with mykb (diff-based)")
	fmt.Fprintln(os.Stderr, "  mykb wiki ingest <file> [--vault DIR] [--host HOST]   ingest a single file")
	fmt.Fprintln(os.Stderr, "  mykb wiki list [--vault DIR]                  list vault inventory")
	fmt.Fprintln(os.Stderr, "  mykb wiki lint [--vault DIR]                  validate vault structure")
}

// Stub implementations — replaced one by one in Tasks 12-16.

func runWikiInit(args []string) {
	fs := flag.NewFlagSet("wiki init", flag.ExitOnError)
	dir := fs.String("vault", ".", "directory to scaffold the vault in")
	name := fs.String("name", "", "wiki name (will appear in URL prefix wiki://<name>/...)")
	fs.Parse(args) //nolint:errcheck

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

func runWikiSync(args []string) {
	fs := flag.NewFlagSet("wiki sync", flag.ExitOnError)
	vaultOverride := fs.String("vault", "", "vault root (default: auto-discover)")
	host := fs.String("host", "", "server address (default: from config)")
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

	// Three-way diff.
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
			if err := callIngest(client, url, string(body), l.hash); err != nil {
				fmt.Fprintf(os.Stderr, "ingest %s: %v\n", url, err)
				continue
			}
			added++
		case existing.hash != l.hash:
			if err := callIngest(client, url, string(body), l.hash); err != nil {
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

func runWikiIngest(args []string) {
	fs := flag.NewFlagSet("wiki ingest", flag.ExitOnError)
	vaultOverride := fs.String("vault", "", "vault root (default: auto-discover)")
	host := fs.String("host", "", "server address (default: from config)")
	fs.Parse(args) //nolint:errcheck

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

func runWikiList(args []string) {
	fmt.Fprintln(os.Stderr, "wiki list: not yet implemented")
	os.Exit(2)
}

func runWikiLint(args []string) {
	fmt.Fprintln(os.Stderr, "wiki lint: not yet implemented")
	os.Exit(2)
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
