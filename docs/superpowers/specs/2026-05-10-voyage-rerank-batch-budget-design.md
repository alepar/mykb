# Voyage rerank batch-budget guard

**Status:** design — pending implementation
**Bug:** `docs/bugs/voyage-rerank-600k-batch-overflow.md`

## Problem

`mykb query` fails with:

```
error: internal: search: rerank: voyage rerank: voyage: bad request,
detail: Request to model 'rerank-2' failed.
The max allowed tokens per submitted batch is 600000.
Your batch has <N> tokens after truncation.
```

Observed `<N>`: 618k–905k tokens. Voyage's rerank-2 caps total batch tokens at
600,000 by the formula `query_tokens × num_docs + sum(doc_tokens)`. Our
`Reranker.Rerank` forwards the candidate slice unchanged, so any sufficiently
large batch overflows and the entire query fails.

`mykb query` is unusable against the current corpus, which breaks the
`wiki-research` workflow's mykb-first retrieval phase.

## Goal

Make `Reranker.Rerank` resilient to the 600k batch limit at any input size.
Preserve recall by splitting overlarge batches across multiple Voyage calls
rather than dropping candidates.

Non-goal: lowering the CLI default for `rerank_depth` — the user wants the
high-recall default and the new guard makes it safe.

## Approach: sub-batch and merge

### Why this is safe

Voyage's reranker is a cross-encoder: each `(query, document)` pair is scored
independently of other pairs in the batch. Scores for the same `(query, doc)`
pair are deterministic across calls (cross-encoder logits → sigmoid). So
splitting a batch into sub-batches and merging by raw score is equivalent to a
single call for ranking purposes.

Voyage's docs are silent on this; we infer it from the model architecture and
treat it as the working assumption. If empirical scores diverge between calls
in practice, the merged top-K will still be a strong superset of the true
top-K.

### Algorithm

1. **Token math:** Reuse `pipeline.countTokens` (export it as `CountTokens`).
   For each document `d_i`, compute its tokens. For the query, compute once.
   - Per-pair cost: `query_tokens + clamp(d_i_tokens, 0, 16000 - query_tokens)`
     — the clamp reflects Voyage's server-side truncation at 16k per pair.
   - Per-batch cost (Voyage's formula): `query_tokens × N + sum(clamped_d_i)`.
2. **Single-call fast path:** if total cost ≤ `MaxBatchTokens` (default 550k —
   8% headroom under Voyage's 600k for tokenizer mismatch), call Voyage once.
3. **Bin-pack:** iterate the candidate list in original (RRF-fused) order,
   greedily packing into sub-batches whose total stays ≤ `MaxBatchTokens`. A
   single oversized doc still gets its own sub-batch (Voyage will truncate it).
   Also respect Voyage's 1000-docs-per-call hard cap.
4. **Parallel calls:** issue all sub-batches concurrently via `errgroup`. Each
   sub-batch returns `[]RerankResult` with indices local to the sub-batch.
5. **Merge:** re-map each sub-batch's local indices to the caller's global
   indices, concatenate all results, sort by score descending, trim to `topK`.

### Per-document handling

We do not truncate documents client-side. Voyage truncates per-pair to 16k
tokens when `truncation=true` (the SDK default). Our token math accounts for
the truncated size by clamping `d_i_tokens` to `16000 - query_tokens` when
budgeting, so a 100k-token chunk contributes ~16k to the budget rather than
100k.

### Constants

- `MaxBatchTokens = 550_000` (8% headroom under Voyage's 600k).
- `MaxDocsPerCall = 1000` (Voyage hard limit).
- `MaxPairTokens = 16_000` (Voyage rerank-2 per-pair limit).

Constants live in `internal/search/rerank.go`. No config surface — these are
properties of the rerank-2 model and only change if we switch models.

### Observability

Log at info level when sub-batching kicks in:

```
voyage rerank: split N candidates into K sub-batches (total ~T tokens, query Q tokens)
```

This keeps the operator aware that recall preservation cost extra API calls
without becoming noisy in the common single-call path.

## Files

### Changed

- `internal/search/rerank.go` — bin-packing, parallel sub-batch calls, merge.
- `internal/search/rerank_test.go` — new unit tests (see Testing below).
- `internal/pipeline/tokencount.go` — rename `countTokens` → `CountTokens` so
  `internal/search` can call it without duplicating the codec setup. Update
  the lone in-package caller (`chunk.go::estimateTokens`) accordingly.

### Unchanged

- `internal/cliconfig/config.go` — `RerankDepth = 1000` stays.
- `internal/server/server.go`, `internal/search/search.go`, proto — no changes.
- `cmd/mykb/main.go` — no changes.

## Testing

Unit tests in `internal/search/rerank_test.go`. The Voyage client is wrapped by
our `Reranker`; we'll introduce a narrow interface for the rerank backend so
tests can inject a fake without hitting the network. (Today `Reranker` holds a
concrete `*voyageai.VoyageClient` — small refactor to accept any
`type rerankBackend interface { Rerank(...) (*voyageai.RerankResponse, error) }`.)

Cases:

1. **Empty input** — returns nil, no backend call.
2. **Small batch under budget** — single backend call, indices and scores match
   what the backend returned, ordered by score desc, trimmed to topK.
3. **Batch over budget, uniform doc sizes** — exactly K sub-batches called,
   each ≤ MaxBatchTokens; merged top-K matches global ordering.
4. **One giant doc among small docs** — giant doc gets its own sub-batch;
   others packed together.
5. **Index mapping correctness** — caller passes 100 docs, sub-batches split
   at 30/30/40, returned `Index` values reference the original 0–99 indexing
   (not local-to-sub-batch).
6. **Backend error in one sub-batch** — first error propagates; other goroutines
   cancel.

E2E manual: rebuild & redeploy `mykb-api`, rerun the bug reproduction:

```bash
mykb query "Bazzite immutable Linux NVIDIA" --no-merge
mykb query "Bazzite immutable Linux NVIDIA" --no-merge --rerank-depth 1000
```

Expected: both succeed, latter logs a split-into-K-sub-batches line.

## Risks

- **Score comparability assumption:** if Voyage's reranker happens to apply
  per-batch normalization (unusual for cross-encoders, but undocumented), the
  merged ordering could diverge from a single-call ordering. Mitigation: in
  practice the divergence would be small score-magnitude shifts, not order
  inversions, so top-K still surfaces the right documents.
- **Cost:** at `rerank-depth=1000` with ~1500-token chunks, expect ~2–3
  parallel rerank calls per query (linear cost). Acceptable per user
  preference to keep high recall default.
- **Tokenizer mismatch:** our `countTokens` uses cl100k_base × 1.2 to
  approximate Voyage's Qwen2 tokenizer. The 8% headroom (550k vs 600k) covers
  typical drift; if a query overflows in practice despite the headroom, lower
  `MaxBatchTokens` further.

## Out of scope

- Investigating why the bug reproduction's `--rerank-depth 1` produced a 752k
  batch. The defensive guard handles this case regardless of root cause. If
  the mystery turns out to be a real plumbing bug, it's a separate fix.
- Lowering CLI default `RerankDepth` (user wants 1000).
- Per-document content truncation in our code (Voyage already truncates).
- Switching to `rerank-2.5` (higher per-pair cap of 32k; same 600k batch cap).
