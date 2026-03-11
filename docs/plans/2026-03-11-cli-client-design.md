# mykb CLI Client Design

## Goal

A standalone CLI binary (`mykb`) for ingesting URLs and querying the knowledge base from client machines, connecting to the mykb gRPC server over the network.

## Architecture

Separate binary at `cmd/mykb/main.go`. The current server binary moves to `cmd/mykb-api/main.go`. No CLI framework — plain `os.Args` subcommand dispatch (only two commands). Connects to the gRPC server as a client using the generated protobuf stubs.

## Configuration

File: `~/.mykb.conf` (TOML format)

```toml
host = "192.168.1.50:9090"
lines = 5
top_k = 10
vector_depth = 1000
fts_depth = 1000
rerank_depth = 1000
```

Resolution order for all settings: CLI flag > TOML config > default value.

## Commands

### `mykb ingest <url> [--quiet] [--host HOST]`

- Calls `IngestURL` server-streaming RPC
- Default: shows live progress with timing (`..crawling..(2.1s)..chunking..(0.3s)..embedding..done.`) using carriage-return overwrite, similar to the meilisearch-movies hybrid search progress reporter
- `--quiet`: suppresses progress, prints `ok` or `error: <message>`
- Exit code 0 on success, 1 on error

### `mykb query <query> [flags]`

Flags:
- `--host HOST` — server address
- `--lines N` — chunk text preview lines (default: 5)
- `--top-k N` — number of results to display (default: 10)
- `--vector-depth N` — candidates from Qdrant (default: 1000)
- `--fts-depth N` — candidates from Meilisearch (default: 1000)
- `--rerank-depth N` — candidates sent to reranker (default: 1000)

Calls `Query` unary RPC, then `GetDocuments` to resolve document metadata (title, URL) for the result set.

Output format (per result):
```
#1 {0.847} Document Title
	https://example.com/page
	chunk text preview, word-wrapped to terminal width,
	up to N lines (configurable), truncated with ...
```

Color scheme (lipgloss):
- Red: rank and score
- White: document title
- Blue: URL
- Gray: chunk text preview

Color auto-detection via lipgloss — respects `NO_COLOR` env var and TTY detection. When called as a tool by an agent, colors are automatically disabled.

Terminal width detection via `golang.org/x/term` for text wrapping, fallback to 80 columns.

## Dependencies

- `google.golang.org/grpc` — already in go.mod
- `github.com/charmbracelet/lipgloss` — terminal styling
- `github.com/BurntSushi/toml` — config file parsing
- `golang.org/x/term` — already in go.mod (indirect)

## Binary Rename

- `cmd/mykb/main.go` → `cmd/mykb-api/main.go` (server)
- `cmd/mykb/main.go` (new) — CLI client
- Dockerfile updated to build `mykb-api`
