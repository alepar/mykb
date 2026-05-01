# LLM-wiki on mykb — Design

**Date:** 2026-04-30
**Status:** Draft for implementation planning

## Background

The [llm-wiki pattern](https://gist.github.com/kennyg/6c45cace2e1c4e424a28fcd51dd6c25b) builds a curated, LLM-generated wiki on top of raw sources: pages for entities, concepts, and synthesis answers, all linked via Obsidian-style wikilinks, indexed by an on-device search tool. The original gist uses [qmd](https://github.com/tobi/qmd) as the search backend.

This design adopts the pattern but uses **mykb** as the search backend instead of qmd. mykb already provides hybrid vector + full-text search over ingested raw sources; we extend it to also ingest and search a curated markdown wiki maintained by an LLM (Claude Code).

## Goals

- Maintain a markdown vault checked into git, viewable in Obsidian, where Claude writes curated entity / concept / synthesis pages.
- Make those pages searchable via mykb's existing hybrid retrieval, alongside raw web sources.
- Keep mykb's data model and pipeline as unchanged as possible (type-blind documents, single chunker).
- Support multiple wikis per machine via URL-prefix namespacing.
- Provide a small, ergonomic CLI surface (`mykb wiki ...`) with vault auto-discovery.

## Non-goals

- No web UI changes for v1 (wiki pages appear with their `wiki://...` URL in existing UI).
- No type-aware ranking or filtering at query time — wiki pages compete with raw chunks on equal footing.
- No automatic re-ingestion daemon or file watcher — sync is explicit.
- No CI/cloud sync — vault and mykb are both local.
- No `sources/` directory in the vault — raw sources live in mykb only.
- No `Index.md` — mykb's search replaces the read-first catalog pattern.

## Architecture

Three actors:

1. **The vault** — a git repository on the user's machine containing markdown files under `entities/`, `concepts/`, `synthesis/`, plus `Log.md`, `CLAUDE.md`, `mykb-wiki.toml`, and `.templates/`. Lives anywhere the user wants; mykb takes its path as input (or auto-discovers).
2. **mykb** — the existing service, extended with a wiki ingestion path that bypasses Crawl4AI (vault files are already markdown). Wiki documents are stored under synthetic URLs `wiki://<wiki-name>/<vault-relative-path>`. They flow through the same chunker, embedder, and dual-index path as raw sources.
3. **The LLM (Claude Code)** — runs from the vault repo, reads/writes markdown files, calls mykb for search, calls `mykb wiki ingest` after writing.

### Data flows

**Write:** LLM edits `concepts/foo.md` → calls `mykb wiki ingest concepts/foo.md` (vault auto-discovered from cwd) → mykb computes URL `wiki://main/concepts/foo.md` and content hash → strips frontmatter → runs the rest of the pipeline (chunk → embed → store in Postgres + Qdrant + Meilisearch).

**Query:** LLM runs `mykb query "..."` (existing path). Results contain both `https://...` URLs (raw sources) and `wiki://...` URLs (wiki pages), distinguished only by URL scheme. For each `wiki://...` hit, the LLM reads the full file from disk via the reconstructed vault path. Frontmatter is included in the on-disk file but absent from the embedded chunks.

**Batch sync:** `mykb wiki sync` walks the vault, computes content hashes, fetches `(url, content_hash)` from mykb for the wiki, and reconciles the three-way diff (new / changed / removed). Changed files are delete-then-ingest (chunk count may differ).

## Data model

No new tables. One schema change: `documents.content_hash text` (nullable). Set for wiki documents (sha256 over the markdown body including frontmatter); NULL for raw-source documents.

The existing unique constraint on `documents.url` is sufficient. Wiki URLs cannot collide with raw-source URLs because the `wiki://` scheme reserves the namespace; wikis cannot collide with each other because the wiki name is in the URL.

The existing `deleteDocument` flow at `internal/server/server.go:271-298` already removes a document from Postgres, Qdrant, Meilisearch, and the filesystem cache. Wiki sync invokes `DeleteDocument` for removed vault files and gets full cleanup for free.

Wiki documents are **not** mirrored into the filesystem cache (`~/.local/share/mykb/`). The vault file is the source of truth; duplicating it on disk has no value. (Raw sources continue to use the cache as today. See `TODO.md` for a broader review of cache utility.)

## Vault structure

```
mykb-wiki-main/
├── mykb-wiki.toml      # vault config (name, optional overrides)
├── CLAUDE.md           # schema and operating rules (travels with the vault)
├── Log.md              # append-only audit trail
├── .templates/
│   ├── entity.md
│   ├── concept.md
│   └── synthesis.md
├── entities/
├── concepts/
└── synthesis/
```

Excluded from sync: `mykb-wiki.toml`, `CLAUDE.md`, `Log.md`, `.templates/`, hidden directories (e.g. `.git/`, `.obsidian/`), anything matching the vault's `.gitignore`.

### `mykb-wiki.toml` schema

```toml
name = "main"             # required; appears in URL prefix wiki://<name>/...
stale_after_days = 180    # optional; default 180. Used by `mykb wiki lint`.
```

Future-extensible (per-vault excludes, custom paths) but kept minimal for v1.

## Page taxonomy and frontmatter

Three page types, distinguished by frontmatter `type:` field. mykb is type-blind; the type is metadata for the LLM and for `mykb wiki lint`.

### Entity — defined by referent

A specific thing in the world: person, organization, product, tool, model, repo, dataset.

```markdown
---
type: entity
kind: model              # person | org | tool | model | repo | dataset | product
aliases: [voyage-context-3, vctx-3]
homepage: https://docs.voyageai.com/docs/contextualized-chunk-embeddings
date_updated: 2026-04-30
---

# voyage-context-3
...body...
```

### Concept — defined by abstraction

An idea, technique, pattern, or algorithm — a *kind of thing*, not a specific instance.

```markdown
---
type: concept
confidence: high          # low | medium | high
related: [chunking, RAG]
date_updated: 2026-04-30
---

# Recursive chunker
...body...
```

### Synthesis — defined by question

The crystallized answer to a specific question. Write-once: never edited to change the answer; superseded by writing a new page and pointing the old one's `superseded_by:` to it.

```markdown
---
type: synthesis
question: "Why did we pick voyage-context-3 over voyage-3?"
answered_at: 2026-04-30
superseded_by: null
sources:
  - https://docs.voyageai.com/docs/contextualized-chunk-embeddings
  - wiki://main/entities/voyage-context-3.md
---

# Why voyage-context-3 over voyage-3?
...body...
```

### Decision rules (encoded in `CLAUDE.md`)

1. Single proper noun as the subject → entity.
2. A *kind of thing* → concept.
3. An answer to a question → synthesis.
4. Unsure between entity and concept → prefer concept (fewer required fields).
5. Synthesis pages are never mutated to change the answer; supersede instead.

### Wikilinks

Two forms supported:

- `[[name]]` — short form; resolves by filename (or `aliases:`) across the vault.
- `[[wiki://main/path/to/page.md|label]]` — explicit form; useful for disambiguation or external `https://...` URLs.

Lint validates short-form references resolve to a unique vault file or alias.

## CLI surface

All `mykb wiki *` subcommands auto-discover the vault by walking up from cwd looking for `mykb-wiki.toml`. If not found: error `not in a wiki vault (no mykb-wiki.toml found from <cwd> upward)`. Optional `--vault <dir>` flag overrides discovery.

| Command | Purpose |
|---|---|
| `mykb wiki init` | Scaffold a new vault: `mykb-wiki.toml`, `Log.md`, `CLAUDE.md`, `.templates/`, empty type directories. Prompts for wiki name. |
| `mykb wiki sync` | Walk vault, diff against mykb by content hash, ingest changed/new files, delete removed files. Prints `+N -M ~K` summary. |
| `mykb wiki ingest <file>` | Ingest a single file by relative path. Idempotent (no-op if hash matches). Used by Claude after writing a page. |
| `mykb wiki lint` | Validate frontmatter, wikilinks, orphans, stale pages. Exit 1 on errors; warnings always print. |
| `mykb wiki list` | Print vault inventory `(path, type, title)` — useful for the LLM to spot-check coverage. |

### Sync algorithm

1. Walk vault for `**/*.md` (excluding `Log.md`, `CLAUDE.md`, `mykb-wiki.toml`, `.templates/`, `.gitignore`-matched paths, and hidden directories).
2. For each file: compute `sha256` of the full body (frontmatter included — changing frontmatter should re-ingest).
3. Call `ListWikiDocuments(wiki_name)` → set of `(url, content_hash)` from mykb.
4. Three-way diff:
   - vault has, mykb doesn't → `IngestMarkdown` (new).
   - both have, hashes differ → `DeleteDocument` then `IngestMarkdown` (changed).
   - mykb has, vault doesn't → `DeleteDocument` (removed).
5. Print summary, exit 0.

### Lint checks

1. **Required frontmatter.** `type` exists and is `entity | concept | synthesis`. `date_updated` exists and parses as a date. Per-type required fields (entity: `kind`; synthesis: `question`, `answered_at`).
2. **Broken wikilinks.** Short-form `[[name]]` must resolve to a unique vault file (matching filename or an `aliases:` entry on an entity page). Explicit-form `[[wiki://...]]` must point to a real vault file. `[[https://...]]` is accepted without verification.
3. **Orphans.** Pages with no inbound wikilinks. Excluded from check: `Log.md`, superseded synthesis pages.
4. **Stale pages.** `date_updated` older than `stale_after_days` (default 180). Synthesis pages exempt.

Exit code: 1 if any errors (categories 1, 2); 0 otherwise. Warnings always print but don't fail. `--format=json` for machine-readable output.

## API surface (proto)

```proto
service KBService {
  // ... existing RPCs ...

  rpc IngestMarkdown(IngestMarkdownRequest) returns (IngestProgress);
  rpc ListWikiDocuments(ListWikiDocumentsRequest) returns (ListWikiDocumentsResponse);
}

message IngestMarkdownRequest {
  string url = 1;          // synthetic, e.g. wiki://main/concepts/foo.md
  string title = 2;        // optional; otherwise extracted from first heading or filename
  string body = 3;         // markdown with frontmatter
  string content_hash = 4; // sha256, used for idempotent re-ingest
}

message ListWikiDocumentsRequest {
  string wiki_name = 1;
}

message ListWikiDocumentsResponse {
  repeated WikiDocument documents = 1;
}

message WikiDocument {
  string url = 1;
  string content_hash = 2;
}
```

## Pipeline changes

The ingestion pipeline gains one branch keyed off URL prefix `wiki://`:

1. **Skip Crawl4AI.** Body comes from the request, not the web.
2. **Strip frontmatter** before chunking. Implementation: detect leading `---\n...\n---\n`, remove it, pass the remainder to the recursive chunker. The original body (with frontmatter) is what gets stored in `documents.content`, so a `GetDocuments(include_content=true)` call returns the full file as the LLM expects.
3. **Skip filesystem cache.** Don't write to `~/.local/share/mykb/`; the vault file is canonical.

Everything else — chunking, contextualized embedding via voyage-context-3, dual-index storage in Qdrant + Meilisearch, RRF + rerank + RSE on query — is unchanged.

`IngestMarkdown` is idempotent: if a document with the same `url` and matching `content_hash` already exists, it returns success without re-embedding (avoiding API spend on no-op syncs).

## Daily workflow

**Bootstrap (one-time):**

1. `mkdir ~/AleCode/mykb-wiki-main && cd $_ && git init`
2. `mykb wiki init` (prompts for wiki name; scaffolds the vault).
3. (Optional) Open the dir in Obsidian.
4. `git add -A && git commit -m "init wiki"`

**Per-question:**

1. User asks a question.
2. Claude runs `mykb query "..."`. Results include `wiki://main/...` and `https://...` URLs.
3. For wiki hits, Claude reads the full file from disk (URL → vault path is mechanical). For raw-source hits, Claude reads the chunked content as-is.
4. Claude answers.
5. If the answer or its components are durable, Claude:
   a. Writes/updates pages in the vault.
   b. Runs `mykb wiki ingest <path>` for each.
   c. Appends a line to `Log.md`.
6. User commits the vault occasionally.

**Cross-machine / after `git pull`:**

`mykb wiki sync` reconciles vault with mykb on the local machine. mykb state is machine-local; sync state lives in `documents.content_hash` and is rebuilt as needed.

## Testing strategy

**Unit tests:**

- URL codec: `vault path ↔ wiki://name/path` round-trip property test.
- Frontmatter parser + stripper: table-driven over realistic fixtures (with/without frontmatter, with code fences containing `---`).
- Vault file walker: `.gitignore` honored, exclude lists honored, hidden dirs skipped, symlinks not followed.
- Wikilink parser: tolerates code fences, escaped brackets, nested constructs.
- Sync diff logic: three-way set diff over mock `(vault, mykb)` inputs.
- Lint checks: one test per check (missing frontmatter, broken link, orphan, stale).
- Vault auto-discovery: walks up to find `mykb-wiki.toml`, errors clearly when absent.

**Integration tests** (existing pattern in mykb):

- End-to-end ingest of a 3-page vault → `mykb query` → assert `wiki://...` URLs appear in results.
- Re-ingest unchanged file → no-op (no duplicate documents in Postgres, no re-embedding API call).
- Edit + re-ingest → old chunks removed from all stores, new chunks present.
- Delete vault file + sync → document removed from Postgres, Qdrant, Meilisearch.
- `IngestMarkdown` with frontmatter → embedded chunks contain only body content; `GetDocuments(include_content=true)` returns the original including frontmatter.

**Smoke tests:**

- Lint fixture vault with one of each error/warning category present; assert reporter output.
- `mykb wiki sync` on a tiny vault end-to-end against a real local mykb stack.

**Healthz extension:** `/healthz/deep` already exercises the full pipeline. Add an optional pass with a `wiki://test/...` URL to cover the wiki ingest path. Tear down the test wiki document afterward.

## Open items deferred to implementation

- Exact wording of `CLAUDE.md` (the vault's operating manual). The Section 5 outline in this doc is the v1 starting point.
- Exact wording of the three `.templates/*.md` files. Drafted from the page-type examples above.
- Whether `mykb wiki list` reads from the filesystem (cheap, simple) or from mykb (consistent with what's actually indexed). Recommend filesystem for v1.
- Whether `Log.md` enforcement (lint warns if a page is modified but `Log.md` has no recent entry mentioning it) is worth adding later.

## Out of scope (TODO.md)

- Filesystem cache utility review. The cache (`~/.local/share/mykb/`) was originally for raw web sources to avoid re-crawling/re-embedding. As the pipeline matured, its value eroded. Wiki documents won't use it. A broader pass should evaluate whether raw sources still need it.
