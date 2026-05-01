package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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
	fmt.Fprintln(os.Stderr, "wiki sync: not yet implemented")
	os.Exit(2)
}

func runWikiIngest(args []string) {
	fmt.Fprintln(os.Stderr, "wiki ingest: not yet implemented")
	os.Exit(2)
}

func runWikiList(args []string) {
	fmt.Fprintln(os.Stderr, "wiki list: not yet implemented")
	os.Exit(2)
}

func runWikiLint(args []string) {
	fmt.Fprintln(os.Stderr, "wiki lint: not yet implemented")
	os.Exit(2)
}
