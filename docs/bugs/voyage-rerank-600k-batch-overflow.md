# Voyage rerank: 600k batch token overflow regardless of depth flags

## Symptom

`mykb query` returns:

```
error: internal: search: rerank: voyage rerank: voyage: bad request,
detail: Request to model 'rerank-2' failed.
The max allowed tokens per submitted batch is 600000.
Your batch has <N> tokens after truncation. Please lower the number of tokens in the batch.
```

Observed `<N>` values: 659,367 / 905,615 / 896,787 / 816,881 / 752,468 / 618,827.

The "after truncation" wording suggests the server is already truncating per-document content and the post-truncation batch is still over Voyage's 600k limit.

## Reproduction

Server: production k3s deployment reachable at `http://api.mykb.k3s` (corpus
size as of 2026-05-10).

Client: `mykb` 2026-05-02 build (`/Users/alepar/.local/bin/mykb`, 30 MB),
config `~/.mykb.conf` with `host = "http://api.mykb.k3s"`.

```bash
# Default depths — 659k tokens
mykb query "Bazzite immutable Linux NVIDIA" --no-merge

# Lowered depths — still 905k / 896k / 816k tokens
mykb query "Bazzite immutable Linux NVIDIA" --no-merge \
    --rerank-depth 200 --vector-depth 200 --fts-depth 200
mykb query "Bazzite immutable Linux NVIDIA" --no-merge \
    --rerank-depth 50  --vector-depth 50  --fts-depth 50

# Floor: top-k=1, depth=1, lines=1 — still 752k tokens
mykb query "Bazzite" --rerank-depth 1 --vector-depth 1 --fts-depth 1 \
    --top-k 1 --lines 1
```

Token count does not decrease monotonically with depth, and even at the
absolute floor (depth 1, top-k 1, lines 1) the batch is 752k tokens.

## Hypothesis

Client-side `--rerank-depth` / `--vector-depth` / `--fts-depth` / `--top-k`
flags do not appear to bound the rerank batch sent to Voyage. Most likely
candidates:

1. The rerank batch is built from a larger candidate set than `rerank-depth`
   suggests (off-by-default, or the flag is being ignored on the server).
2. Per-document content sent to rerank is unbounded — a single very large
   chunk can dominate the batch even at low candidate counts. The
   "after truncation" suggests truncation logic exists but doesn't enforce
   the 600k ceiling.

## Impact

`mykb query` is unusable against the current corpus at any depth setting,
which breaks the `wiki-research` workflow's mykb-first retrieval phase
(falls through to web research without local context).

Fallback used: filesystem `grep` across the vault to confirm no relevant
prior pages, then proceed straight to deep-research.

## Suggested fixes

- Cap rerank batch size at the model's hard limit (Voyage rerank-2 is
  600k tokens) by truncating per-document content or by trimming the
  candidate list before submission.
- Verify `--rerank-depth` is actually plumbed to the rerank step (the
  symptom is consistent with the flag being ignored).
- Consider chunking the rerank call when the batch exceeds the limit
  rather than failing the whole query.

## Context

Filed by the wiki-research run for "Linux OS for ML homelab" research on
2026-05-10 after the rerank failure prevented mykb-first retrieval.

## Resolution

Fixed in commit `5a2b0e1` (2026-05-10). Design and plan:

- `docs/superpowers/specs/2026-05-10-voyage-rerank-batch-budget-design.md`
- `docs/superpowers/plans/2026-05-10-voyage-rerank-batch-budget.md`

`internal/search/rerank.go` now bin-packs the candidate slice into
sub-batches under a 550k-token budget (Voyage's formula
`query_tokens × N + sum(clamped_doc_tokens)`, 8% headroom under 600k),
dispatches them in parallel via `errgroup`, and merges by score. Cross-
encoder scores are comparable across calls, so the merged top-K matches
a single-call ranking.

Verified against the production deployment on 2026-05-10:

```
mykb query "Bazzite immutable Linux NVIDIA" --no-merge
mykb query "Bazzite immutable Linux NVIDIA" --no-merge --rerank-depth 1000
```

Both succeed. Server log line:

```
voyage rerank: split 1000 candidates into 2 sub-batches (query=12 tokens, total docs=802188 tokens)
```

The depth=1-still-752k-tokens reproduction remains unexplained by static
inspection of the current code, but the defensive sub-batching makes it
moot — the batch is always split if it would overflow regardless of the
candidate count.
