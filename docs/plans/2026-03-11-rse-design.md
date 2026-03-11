# Relevant Segment Extraction (RSE) Design

## Goal

After reranking, merge adjacent high-scoring chunks from the same document into coherent segments, returning longer contextual passages instead of isolated chunk snippets.

## Algorithm

RSE runs as a post-reranking step in `HybridSearcher.Search()`:

1. **Build meta-document** — lay out all chunks sequentially per document, tracking document boundaries. E.g., doc A has chunks 0-7 (positions 0-7), doc B has chunks 0-4 (positions 8-12).

2. **Score each position** — for each chunk in the meta-document:
   ```
   value = exp(-rank / decay_rate) * rerank_score - irrelevant_chunk_penalty
   ```
   Chunks not in the top-K results get value = `-irrelevant_chunk_penalty` (negative, discouraging inclusion).

3. **Greedy segment selection** — find the contiguous segment with the highest sum of values. Add it to results. Repeat, skipping overlapping segments and document boundaries, until budget exhausted or no segment exceeds `minimum_value`.

## Proto Changes

```protobuf
message QueryRequest {
  ...
  bool no_merge = 6;  // skip RSE, return individual chunks
}

message QueryResult {
  ...
  int32 chunk_index_end = 6;  // exclusive end index (RSE segments)
}
```

When `no_merge=false` (default), results are merged segments. `chunk_index_end` indicates the range (e.g., chunk_index=3, chunk_index_end=6 means chunks 3,4,5). For individual chunks (no_merge or single-chunk segments), chunk_index_end = chunk_index + 1.

## Implementation Location

New file `internal/search/rse.go` with a pure function:

```go
func ExtractSegments(results []RankedChunk, params RSEParams) []Segment
```

Called from `HybridSearcher.Search()` after reranking, before returning results. When `no_merge=true`, this step is skipped.

The server reads chunk texts for the segment ranges from the filesystem and concatenates them into the result `text` field.

## Parameters

"Balanced" defaults, configurable via server config (not CLI flags):

| Parameter | Default |
|---|---|
| `max_length` | 15 chunks |
| `overall_max_length` | 30 chunks |
| `minimum_value` | 0.5 |
| `irrelevant_chunk_penalty` | 0.18 |
| `decay_rate` | 30 |

## CLI Changes

- `--no-merge` flag on `mykb query` passes `no_merge=true` to the server
- Default behavior shows merged segments
- Display shows chunk range (e.g., `3-5/8` instead of `4/8`)
