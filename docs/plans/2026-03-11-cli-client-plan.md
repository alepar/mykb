# CLI Client Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a standalone `mykb` CLI binary with `ingest` and `query` subcommands that connect to the mykb gRPC server.

**Architecture:** Separate binary at `cmd/mykb/main.go`, plain `os.Args` dispatch (no framework). Current server moves to `cmd/mykb-api/main.go`. Config loaded from `~/.mykb.conf` (TOML) with CLI flag overrides. Query results include chunk text returned directly from the gRPC server (added to proto).

**Tech Stack:** Go 1.25, gRPC client, lipgloss (terminal colors), BurntSushi/toml (config), golang.org/x/term (terminal width)

---

### Task 1: Rename server binary from `cmd/mykb` to `cmd/mykb-api`

**Files:**
- Rename: `cmd/mykb/main.go` → `cmd/mykb-api/main.go`
- Modify: `Dockerfile:6` (build output path)
- Modify: `Dockerfile:9` (COPY path)
- Modify: `Dockerfile:12` (CMD)

**Step 1: Move the server binary directory**

```bash
git mv cmd/mykb cmd/mykb-api
```

**Step 2: Update Dockerfile to build and run `mykb-api`**

Current Dockerfile lines 6, 9, 12:
```dockerfile
RUN CGO_ENABLED=0 go build -o /mykb ./cmd/mykb
...
COPY --from=builder /mykb /mykb
...
CMD ["/mykb"]
```

Change to:
```dockerfile
RUN CGO_ENABLED=0 go build -o /mykb-api ./cmd/mykb-api
...
COPY --from=builder /mykb-api /mykb-api
...
CMD ["/mykb-api"]
```

**Step 3: Verify it builds**

Run: `go build ./cmd/mykb-api/`
Expected: Builds successfully, produces `mykb-api` binary

**Step 4: Commit**

```bash
git add cmd/ Dockerfile
git commit -m "refactor: rename server binary from mykb to mykb-api"
```

---

### Task 2: Add chunk text to QueryResult proto

The CLI needs chunk text for display. The server already reads chunk text during search (for reranking in `internal/search/search.go:172`). Instead of making the CLI do extra RPCs, add `text` to the proto response and have the server populate it.

**Files:**
- Modify: `proto/mykb/v1/kb.proto:37-42` (add `text` field to `QueryResult`)
- Regenerate: `gen/mykb/v1/kb.pb.go`, `gen/mykb/v1/kb_grpc.pb.go`
- Modify: `internal/search/search.go:54-59` (add `Text` to `SearchResult`)
- Modify: `internal/search/search.go:186-198` (populate `Text` from `chunkTexts`)
- Modify: `internal/server/server.go:99-107` (pass `Text` to proto)

**Step 1: Add `text` field to QueryResult proto**

In `proto/mykb/v1/kb.proto`, change `QueryResult` from:
```protobuf
message QueryResult {
  string chunk_id = 1;
  string document_id = 2;
  int32 chunk_index = 3;
  float score = 4;
}
```
to:
```protobuf
message QueryResult {
  string chunk_id = 1;
  string document_id = 2;
  int32 chunk_index = 3;
  float score = 4;
  string text = 5;
}
```

**Step 2: Regenerate protobuf code**

Run: `just proto`
Expected: Regenerates `gen/mykb/v1/kb.pb.go` and `gen/mykb/v1/kb_grpc.pb.go` without errors.

**Step 3: Add `Text` field to `SearchResult` struct**

In `internal/search/search.go`, change the `SearchResult` struct from:
```go
type SearchResult struct {
	ChunkID    string
	DocumentID string
	ChunkIndex int
	Score      float64
}
```
to:
```go
type SearchResult struct {
	ChunkID    string
	DocumentID string
	ChunkIndex int
	Score      float64
	Text       string
}
```

**Step 4: Populate `Text` in search results**

In `internal/search/search.go`, in the `Search` method, change the result mapping block (around line 186) from:
```go
results := make([]SearchResult, len(reranked))
for i, rr := range reranked {
    id := candidateIDs[rr.Index]
    meta := metaMap[id]
    results[i] = SearchResult{
        ChunkID:    id,
        DocumentID: meta.DocumentID,
        ChunkIndex: meta.ChunkIndex,
        Score:      rr.Score,
    }
}
```
to:
```go
results := make([]SearchResult, len(reranked))
for i, rr := range reranked {
    id := candidateIDs[rr.Index]
    meta := metaMap[id]
    results[i] = SearchResult{
        ChunkID:    id,
        DocumentID: meta.DocumentID,
        ChunkIndex: meta.ChunkIndex,
        Score:      rr.Score,
        Text:       chunkTexts[rr.Index],
    }
}
```

**Step 5: Pass `Text` to proto in server**

In `internal/server/server.go`, change the query result mapping (around line 101) from:
```go
protoResults[i] = &mykbv1.QueryResult{
    ChunkId:    r.ChunkID,
    DocumentId: r.DocumentID,
    ChunkIndex: int32(r.ChunkIndex),
    Score:      float32(r.Score),
}
```
to:
```go
protoResults[i] = &mykbv1.QueryResult{
    ChunkId:    r.ChunkID,
    DocumentId: r.DocumentID,
    ChunkIndex: int32(r.ChunkIndex),
    Score:      float32(r.Score),
    Text:       r.Text,
}
```

**Step 6: Verify everything builds**

Run: `go build ./...`
Expected: All packages build successfully.

**Step 7: Run tests**

Run: `go test ./...`
Expected: All tests pass.

**Step 8: Commit**

```bash
git add proto/ gen/ internal/search/search.go internal/server/server.go
git commit -m "feat: include chunk text in query results"
```

---

### Task 3: Add new dependencies (lipgloss, toml)

**Step 1: Add dependencies**

Run: `go get github.com/charmbracelet/lipgloss github.com/BurntSushi/toml`
Expected: `go.mod` and `go.sum` updated.

**Step 2: Verify**

Run: `go mod tidy`
Expected: No errors.

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add lipgloss and toml"
```

---

### Task 4: CLI config loading (`internal/cliconfig/config.go`)

**Files:**
- Create: `internal/cliconfig/config.go`
- Create: `internal/cliconfig/config_test.go`

**Step 1: Write the test**

Create `internal/cliconfig/config_test.go`:

```go
package cliconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load("")
	if cfg.Host != "localhost:9090" {
		t.Errorf("Host = %q, want %q", cfg.Host, "localhost:9090")
	}
	if cfg.Lines != 5 {
		t.Errorf("Lines = %d, want 5", cfg.Lines)
	}
	if cfg.TopK != 10 {
		t.Errorf("TopK = %d, want 10", cfg.TopK)
	}
	if cfg.VectorDepth != 1000 {
		t.Errorf("VectorDepth = %d, want 1000", cfg.VectorDepth)
	}
	if cfg.FTSDepth != 1000 {
		t.Errorf("FTSDepth = %d, want 1000", cfg.FTSDepth)
	}
	if cfg.RerankDepth != 1000 {
		t.Errorf("RerankDepth = %d, want 1000", cfg.RerankDepth)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "mykb.conf")
	err := os.WriteFile(confPath, []byte(`
host = "myserver:9090"
lines = 3
top_k = 20
vector_depth = 500
fts_depth = 500
rerank_depth = 200
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg := Load(confPath)
	if cfg.Host != "myserver:9090" {
		t.Errorf("Host = %q, want %q", cfg.Host, "myserver:9090")
	}
	if cfg.Lines != 3 {
		t.Errorf("Lines = %d, want 3", cfg.Lines)
	}
	if cfg.TopK != 20 {
		t.Errorf("TopK = %d, want 20", cfg.TopK)
	}
	if cfg.VectorDepth != 500 {
		t.Errorf("VectorDepth = %d, want 500", cfg.VectorDepth)
	}
}

func TestPartialOverride(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "mykb.conf")
	err := os.WriteFile(confPath, []byte(`host = "custom:9090"`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg := Load(confPath)
	if cfg.Host != "custom:9090" {
		t.Errorf("Host = %q, want %q", cfg.Host, "custom:9090")
	}
	// Unset values should be defaults
	if cfg.Lines != 5 {
		t.Errorf("Lines = %d, want 5", cfg.Lines)
	}
	if cfg.TopK != 10 {
		t.Errorf("TopK = %d, want 10", cfg.TopK)
	}
}

func TestMissingFileUsesDefaults(t *testing.T) {
	cfg := Load("/nonexistent/path/mykb.conf")
	if cfg.Host != "localhost:9090" {
		t.Errorf("Host = %q, want %q", cfg.Host, "localhost:9090")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cliconfig/`
Expected: FAIL — package doesn't exist yet.

**Step 3: Write the implementation**

Create `internal/cliconfig/config.go`:

```go
package cliconfig

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds CLI client configuration.
type Config struct {
	Host        string `toml:"host"`
	Lines       int    `toml:"lines"`
	TopK        int    `toml:"top_k"`
	VectorDepth int    `toml:"vector_depth"`
	FTSDepth    int    `toml:"fts_depth"`
	RerankDepth int    `toml:"rerank_depth"`
}

// DefaultConfigPath returns ~/.mykb.conf.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mykb.conf")
}

// Load reads config from the given path, applying defaults for any missing fields.
// If path is empty, it tries DefaultConfigPath(). Missing files are silently ignored.
func Load(path string) Config {
	cfg := Config{
		Host:        "localhost:9090",
		Lines:       5,
		TopK:        10,
		VectorDepth: 1000,
		FTSDepth:    1000,
		RerankDepth: 1000,
	}

	if path == "" {
		path = DefaultConfigPath()
	}

	if path != "" {
		// toml.DecodeFile overwrites only fields present in the file;
		// defaults remain for missing fields.
		_, _ = toml.DecodeFile(path, &cfg)
	}

	return cfg
}
```

**Step 4: Run tests**

Run: `go test ./internal/cliconfig/`
Expected: All 4 tests pass.

**Step 5: Commit**

```bash
git add internal/cliconfig/
git commit -m "feat: add CLI config loading from ~/.mykb.conf"
```

---

### Task 5: CLI main with subcommand dispatch and flag parsing

**Files:**
- Create: `cmd/mykb/main.go`

**Step 1: Create the CLI entry point**

Create `cmd/mykb/main.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mykbv1 "mykb/gen/mykb/v1"
	"mykb/internal/cliconfig"
)

var (
	redStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	whiteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	blueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	grayStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "ingest":
		runIngest(os.Args[2:])
	case "query":
		runQuery(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  mykb ingest <url> [--quiet] [--host HOST]")
	fmt.Fprintln(os.Stderr, "  mykb query <query> [--host HOST] [--lines N] [--top-k N] [--vector-depth N] [--fts-depth N] [--rerank-depth N]")
}

func connect(host string) (*grpc.ClientConn, error) {
	return grpc.NewClient(host, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// --- ingest command ---

func runIngest(args []string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "suppress progress, print ok/error only")
	host := fs.String("host", "", "server address (default: from config)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: mykb ingest <url> [--quiet] [--host HOST]")
		os.Exit(1)
	}
	url := fs.Arg(0)

	cfg := cliconfig.Load("")
	if *host != "" {
		cfg.Host = *host
	}

	conn, err := connect(cfg.Host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := mykbv1.NewKBServiceClient(conn)
	stream, err := client.IngestURL(context.Background(), &mykbv1.IngestURLRequest{Url: url})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var lastStatus string
	var stepStart time.Time
	var progress strings.Builder

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			if *quiet {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			}
			os.Exit(1)
		}

		if *quiet {
			continue
		}

		status := msg.GetStatus()
		if status != lastStatus {
			// Close previous step with timing
			if lastStatus != "" {
				elapsed := time.Since(stepStart)
				fmt.Fprintf(&progress, "(%.1fs)", elapsed.Seconds())
			}
			fmt.Fprintf(&progress, "..%s..", status)
			lastStatus = status
			stepStart = time.Now()
		}

		fmt.Fprintf(os.Stderr, "\r%s", progress.String())
	}

	if *quiet {
		fmt.Println("ok")
	} else {
		// Close final step with timing
		if lastStatus != "" {
			elapsed := time.Since(stepStart)
			fmt.Fprintf(&progress, "(%.1fs)", elapsed.Seconds())
		}
		fmt.Fprintf(os.Stderr, "\r%s done.\n", progress.String())
	}
}

// --- query command ---

func runQuery(args []string) {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	host := fs.String("host", "", "server address (default: from config)")
	lines := fs.Int("lines", 0, "chunk text preview lines")
	topK := fs.Int("top-k", 0, "number of results to display")
	vectorDepth := fs.Int("vector-depth", 0, "candidates from Qdrant")
	ftsDepth := fs.Int("fts-depth", 0, "candidates from Meilisearch")
	rerankDepth := fs.Int("rerank-depth", 0, "candidates sent to reranker")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: mykb query <query> [flags]")
		os.Exit(1)
	}
	query := fs.Arg(0)

	cfg := cliconfig.Load("")
	if *host != "" {
		cfg.Host = *host
	}
	if *lines > 0 {
		cfg.Lines = *lines
	}
	if *topK > 0 {
		cfg.TopK = *topK
	}
	if *vectorDepth > 0 {
		cfg.VectorDepth = *vectorDepth
	}
	if *ftsDepth > 0 {
		cfg.FTSDepth = *ftsDepth
	}
	if *rerankDepth > 0 {
		cfg.RerankDepth = *rerankDepth
	}

	conn, err := connect(cfg.Host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := mykbv1.NewKBServiceClient(conn)

	resp, err := client.Query(context.Background(), &mykbv1.QueryRequest{
		Query:       query,
		TopK:        int32(cfg.TopK),
		VectorDepth: int32(cfg.VectorDepth),
		FtsDepth:    int32(cfg.FTSDepth),
		RerankDepth: int32(cfg.RerankDepth),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Results) == 0 {
		fmt.Println("no results")
		return
	}

	// Resolve document metadata (title, URL) via GetDocuments
	docIDs := uniqueDocIDs(resp.Results)
	docsResp, err := client.GetDocuments(context.Background(), &mykbv1.GetDocumentsRequest{
		Ids: docIDs,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching documents: %v\n", err)
		os.Exit(1)
	}
	docMap := make(map[string]*mykbv1.Document, len(docsResp.Documents))
	for _, doc := range docsResp.Documents {
		docMap[doc.Id] = doc
	}

	termWidth := getTerminalWidth()

	for i, result := range resp.Results {
		doc := docMap[result.DocumentId]

		title := result.DocumentId
		url := ""
		if doc != nil {
			if doc.Title != "" {
				title = doc.Title
			}
			url = doc.Url
		}

		// Line 1: #N {score} Title
		indexScore := fmt.Sprintf("#%d {%.3f} ", i+1, result.Score)
		fmt.Print(redStyle.Render(indexScore))
		fmt.Println(whiteStyle.Render(title))

		// Line 2: URL
		if url != "" {
			fmt.Println(blueStyle.Render("\t" + url))
		}

		// Line 3+: chunk text preview
		if result.Text != "" {
			preview := formatTextPreview(result.Text, termWidth, cfg.Lines)
			fmt.Println(grayStyle.Render(preview))
		}

		fmt.Println()
	}
}

func uniqueDocIDs(results []*mykbv1.QueryResult) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, r := range results {
		if !seen[r.DocumentId] {
			seen[r.DocumentId] = true
			ids = append(ids, r.DocumentId)
		}
	}
	return ids
}

func getTerminalWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		return 80
	}
	return width
}

func formatTextPreview(text string, termWidth, maxLines int) string {
	// Collapse whitespace
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	text = strings.TrimSpace(text)

	indentWidth := 8 // tab width
	availableWidth := termWidth - indentWidth
	if availableWidth < 40 {
		availableWidth = 40
	}

	words := strings.Fields(text)
	var lines []string
	var currentLine strings.Builder
	truncated := false

	for _, word := range words {
		if currentLine.Len() == 0 {
			currentLine.WriteString(word)
			continue
		}
		if currentLine.Len()+1+len(word) <= availableWidth {
			currentLine.WriteString(" ")
			currentLine.WriteString(word)
			continue
		}
		lines = append(lines, currentLine.String())
		if len(lines) >= maxLines {
			truncated = true
			break
		}
		currentLine.Reset()
		currentLine.WriteString(word)
	}

	if currentLine.Len() > 0 && len(lines) < maxLines {
		lines = append(lines, currentLine.String())
	}

	var result strings.Builder
	for i, line := range lines {
		result.WriteString("\t")
		result.WriteString(line)
		if i < len(lines)-1 {
			result.WriteString("\n")
		}
	}
	if truncated {
		result.WriteString("...")
	}

	return result.String()
}
```

**Step 2: Verify it builds**

Run: `go build ./cmd/mykb/`
Expected: Builds successfully.

**Step 3: Commit**

```bash
git add cmd/mykb/
git commit -m "feat: add mykb CLI with ingest and query commands"
```

---

### Task 6: Update Justfile and verify full build

**Files:**
- Modify: `Justfile`

**Step 1: Update Justfile build target**

The `build` target currently runs `go build ./...` which will build both binaries. No change needed there. But add a convenience target for the CLI:

Add to `Justfile` after the existing `build:` target:
```
cli:
    go build -o mykb ./cmd/mykb/
```

**Step 2: Verify full build and tests**

Run: `go build ./... && go test ./...`
Expected: All packages build, all tests pass.

**Step 3: Commit**

```bash
git add Justfile
git commit -m "chore: add cli build target to Justfile"
```

---

### Task 7: Manual integration test

This task verifies the CLI works end-to-end against the running Docker Compose stack.

**Step 1: Build the CLI**

Run: `go build -o mykb ./cmd/mykb/`

**Step 2: Test ingest command**

Run: `./mykb ingest https://go.dev/doc/effective_go`
Expected: Progress output like `..CRAWLING..(X.Xs)..CHUNKING..(X.Xs)..EMBEDDING..(X.Xs)..INDEXING..(X.Xs) done.`

**Step 3: Test ingest with --quiet**

Run: `./mykb ingest https://go.dev/blog/defer-panic-and-recover --quiet`
Expected: `ok`

**Step 4: Test query command**

Run: `./mykb query "how do goroutines work"`
Expected: Colored output with ranked results showing title, URL, and chunk text preview.

**Step 5: Test query with custom flags**

Run: `./mykb query "error handling in go" --top-k 3 --lines 2`
Expected: 3 results with 2-line chunk previews.
