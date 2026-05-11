# Voyage rerank batch-budget guard — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `mykb query` from failing with Voyage's 600k batch-token error by sub-batching overlarge rerank requests in parallel and merging by score.

**Architecture:** Inside `Reranker.Rerank`, compute Voyage's batch-token formula (`query_tokens × N + sum(clamped_doc_tokens)`) before calling the API. If under a safe budget (550k), one call. If over, greedy bin-pack into sub-batches each ≤ 550k tokens and ≤ 1000 docs, run them in parallel via `errgroup`, merge the per-batch results by score (with local→global index mapping), sort descending, trim to `topK`.

**Tech Stack:** Go, `github.com/austinfhunter/voyageai` SDK, `golang.org/x/sync/errgroup`, existing `pipeline.countTokens` (cl100k_base × 1.2).

**Spec:** `docs/superpowers/specs/2026-05-10-voyage-rerank-batch-budget-design.md`

---

## File Structure

**Changed:**
- `internal/pipeline/tokencount.go` — export `countTokens` as `CountTokens`.
- `internal/pipeline/chunk.go` — update lone caller `estimateTokens`.
- `internal/search/rerank.go` — interface for backend, bin-packing helper, parallel sub-batch dispatch, merge.
- `internal/search/rerank_test.go` — fake backend + new unit tests for splitting/merging/indexing.

**Not changed:**
- `internal/cliconfig/config.go` (RerankDepth stays 1000 per design).
- `internal/search/search.go`, `internal/server/server.go`, `cmd/mykb/main.go`, proto.

---

## Task 1: Export `CountTokens` from `internal/pipeline`

**Files:**
- Modify: `internal/pipeline/tokencount.go`
- Modify: `internal/pipeline/chunk.go:27` (the `estimateTokens` wrapper)

Tiny rename so `internal/search` can compute Voyage-equivalent tokens without duplicating the codec setup.

- [ ] **Step 1: Rename the function**

In `internal/pipeline/tokencount.go`, change:

```go
// countTokens returns an estimated token count for a string using the
// cl100k_base BPE tokenizer with a 1.2x multiplier to approximate
// Voyage AI's Qwen2 tokenizer. Falls back to len/4 if the tokenizer
// fails to load.
func countTokens(s string) int {
```

to:

```go
// CountTokens returns an estimated token count for a string using the
// cl100k_base BPE tokenizer with a 1.2x multiplier to approximate
// Voyage AI's Qwen2 tokenizer. Falls back to len/4 if the tokenizer
// fails to load.
func CountTokens(s string) int {
```

- [ ] **Step 2: Update the in-package caller**

In `internal/pipeline/chunk.go` around line 26-28, change:

```go
func estimateTokens(s string) int {
	return countTokens(s)
}
```

to:

```go
func estimateTokens(s string) int {
	return CountTokens(s)
}
```

- [ ] **Step 3: Verify build and tests still pass**

Run: `go build ./... && go test ./internal/pipeline/...`
Expected: build succeeds, all pipeline tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/pipeline/tokencount.go internal/pipeline/chunk.go
git commit -m "refactor: export CountTokens for cross-package use

Renames countTokens to CountTokens so internal/search can reuse the
Voyage-calibrated tokenizer without duplicating the codec setup.
Behavior unchanged."
```

---

## Task 2: Introduce `rerankBackend` interface for testability

**Files:**
- Modify: `internal/search/rerank.go`

Today `Reranker` holds a concrete `*voyageai.VoyageClient`. We need a narrow interface so tests can inject a fake without touching the network. No behavior change in this task — just a refactor with an adapter that delegates to the real SDK.

- [ ] **Step 1: Replace the struct with an interface + adapter**

Replace the contents of `internal/search/rerank.go` (currently 78 lines) with:

```go
package search

import (
	"context"
	"fmt"
	"sort"

	"github.com/austinfhunter/voyageai"
)

// rerankBackend is the narrow surface we use from the Voyage SDK. Tests
// substitute fakes; production wires the real client via voyageBackend.
type rerankBackend interface {
	Rerank(query string, documents []string, model string, opts *voyageai.RerankRequestOpts) (*voyageai.RerankResponse, error)
}

// voyageBackend adapts *voyageai.VoyageClient to rerankBackend.
type voyageBackend struct {
	client *voyageai.VoyageClient
}

func (v *voyageBackend) Rerank(query string, documents []string, model string, opts *voyageai.RerankRequestOpts) (*voyageai.RerankResponse, error) {
	return v.client.Rerank(query, documents, model, opts)
}

// Reranker re-scores documents against a query using the Voyage AI rerank API.
type Reranker struct {
	backend rerankBackend
	model   string
}

// RerankResult holds the original index of a document in the input slice
// and its relevance score assigned by the reranker.
type RerankResult struct {
	Index int
	Score float64
}

// NewReranker creates a Reranker that calls the Voyage AI rerank API.
// If model is empty it defaults to "rerank-2".
func NewReranker(apiKey, model string) *Reranker {
	if model == "" {
		model = "rerank-2"
	}
	client := voyageai.NewClient(&voyageai.VoyageClientOpts{Key: apiKey})
	return &Reranker{
		backend: &voyageBackend{client: client},
		model:   model,
	}
}

// Rerank sends the query and documents to the Voyage AI rerank endpoint and
// returns the top-K results sorted by relevance score in descending order.
// Each RerankResult carries the original index of the document in the input
// slice together with the relevance score.
func (r *Reranker) Rerank(ctx context.Context, query string, documents []string, topK int) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	var tk *int
	if topK > 0 {
		tk = &topK
	}

	resp, err := r.backend.Rerank(query, documents, r.model, &voyageai.RerankRequestOpts{
		TopK: tk,
	})
	if err != nil {
		return nil, fmt.Errorf("voyage rerank: %w", err)
	}

	results := make([]RerankResult, len(resp.Data))
	for i, obj := range resp.Data {
		results[i] = RerankResult{
			Index: obj.Index,
			Score: float64(obj.RelevanceScore),
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}
```

- [ ] **Step 2: Update the existing test that pokes at internals**

`internal/search/rerank_test.go:50` references `r.client`. Change it to `r.backend`:

Open `internal/search/rerank_test.go` and replace the body of `TestNewRerankerDefaults` and `TestNewRerankerCustomModel` accordingly:

```go
func TestNewRerankerDefaults(t *testing.T) {
	r := NewReranker("fake-key", "")
	if r.model != "rerank-2" {
		t.Fatalf("expected default model rerank-2, got %s", r.model)
	}
	if r.backend == nil {
		t.Fatal("backend should not be nil")
	}
}

func TestNewRerankerCustomModel(t *testing.T) {
	r := NewReranker("fake-key", "rerank-2-lite")
	if r.model != "rerank-2-lite" {
		t.Fatalf("expected model rerank-2-lite, got %s", r.model)
	}
}
```

- [ ] **Step 3: Verify build and tests still pass**

Run: `go build ./... && go test ./internal/search/...`
Expected: build succeeds, all search tests pass (integration test stays skipped without `TEST_VOYAGE_API_KEY`).

- [ ] **Step 4: Commit**

```bash
git add internal/search/rerank.go internal/search/rerank_test.go
git commit -m "refactor: extract rerankBackend interface for test injection

Wraps the concrete *voyageai.VoyageClient behind a narrow interface so
upcoming sub-batching tests can use a fake backend without hitting the
network. Behavior unchanged."
```

---

## Task 3: Add the budget bin-packing helper (TDD)

**Files:**
- Modify: `internal/search/rerank.go` (add helper + constants)
- Modify: `internal/search/rerank_test.go` (add helper tests)

Pure function that splits a doc list into sub-batch index-groups based on Voyage's budget formula. No I/O, easy to test in isolation.

- [ ] **Step 1: Write the failing tests**

Append to `internal/search/rerank_test.go`:

```go
func TestSplitIntoBatches_UnderBudget(t *testing.T) {
	// 1-token query, 10 docs of 100 tokens each.
	// cost = 1*10 + 10*100 = 1010 tokens, well under 550k.
	docTokens := make([]int, 10)
	for i := range docTokens {
		docTokens[i] = 100
	}
	groups := splitIntoBatches(1, docTokens, 550_000, 1000)
	if len(groups) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(groups))
	}
	if len(groups[0]) != 10 {
		t.Fatalf("expected 10 docs in batch, got %d", len(groups[0]))
	}
	for i, idx := range groups[0] {
		if idx != i {
			t.Fatalf("expected index %d at position %d, got %d", i, i, idx)
		}
	}
}

func TestSplitIntoBatches_OverBudget(t *testing.T) {
	// 1-token query, 1000 docs of 1500 tokens each.
	// per-doc cost (clamped at 16000-1=15999, no clamp triggers) = 1500
	// per-batch cost: 1*N + N*1500 = 1501*N
	// budget 550_000 ⇒ N ≤ 366 per batch
	docTokens := make([]int, 1000)
	for i := range docTokens {
		docTokens[i] = 1500
	}
	groups := splitIntoBatches(1, docTokens, 550_000, 1000)
	if len(groups) < 2 {
		t.Fatalf("expected at least 2 batches, got %d", len(groups))
	}
	// All indices accounted for, in order, no duplicates.
	seen := 0
	for _, g := range groups {
		for _, idx := range g {
			if idx != seen {
				t.Fatalf("expected index %d, got %d", seen, idx)
			}
			seen++
		}
	}
	if seen != 1000 {
		t.Fatalf("expected 1000 indices total, got %d", seen)
	}
	// Each batch respects the budget.
	for bi, g := range groups {
		cost := 1*len(g)
		for _, idx := range g {
			cost += docTokens[idx]
		}
		if cost > 550_000 {
			t.Fatalf("batch %d cost %d exceeds budget", bi, cost)
		}
	}
}

func TestSplitIntoBatches_GiantDocGetsOwnBatch(t *testing.T) {
	// One enormous doc (clamps to 16k-1=15999) among small docs.
	// Even a single 15999-token doc fits under 550k, so it doesn't
	// strictly need its own batch — but a doc bigger than the budget
	// must still be sent as a singleton (with truncation server-side).
	// Simulate by using a budget smaller than one doc's clamped cost.
	docTokens := []int{50, 50, 100_000, 50, 50}
	// With query=10, clamped per-doc = min(d, 16000-10=15990)
	// doc[2] clamps to 15990. Use a tiny budget to force per-doc isolation.
	groups := splitIntoBatches(10, docTokens, 16_000, 1000)
	// Expect doc[2] (or any doc whose clamped cost alone exceeds budget) on its own.
	// In this scenario per-doc clamped costs are: 50, 50, 15990, 50, 50.
	// Budget 16000. Cost formula per batch with N docs: 10*N + sum(clamped).
	// doc[2] alone: 10*1 + 15990 = 16000 ≤ 16000 — fits as singleton.
	// Adding any other doc would push over. So expect doc[2] alone.
	foundGiantAlone := false
	for _, g := range groups {
		if len(g) == 1 && g[0] == 2 {
			foundGiantAlone = true
		}
	}
	if !foundGiantAlone {
		t.Fatalf("expected doc[2] to land in its own batch; groups=%v", groups)
	}
	// All indices present in order.
	seen := 0
	for _, g := range groups {
		for _, idx := range g {
			if idx != seen {
				t.Fatalf("expected index %d, got %d", seen, idx)
			}
			seen++
		}
	}
}

func TestSplitIntoBatches_MaxDocsPerCall(t *testing.T) {
	// Tiny docs but lots of them — should hit the max-docs cap, not the
	// token budget.
	docTokens := make([]int, 2500)
	for i := range docTokens {
		docTokens[i] = 1
	}
	groups := splitIntoBatches(1, docTokens, 550_000, 1000)
	if len(groups) != 3 {
		t.Fatalf("expected 3 batches (1000+1000+500), got %d", len(groups))
	}
	if len(groups[0]) != 1000 || len(groups[1]) != 1000 || len(groups[2]) != 500 {
		t.Fatalf("unexpected batch sizes: %d, %d, %d", len(groups[0]), len(groups[1]), len(groups[2]))
	}
}

func TestSplitIntoBatches_Empty(t *testing.T) {
	groups := splitIntoBatches(1, nil, 550_000, 1000)
	if len(groups) != 0 {
		t.Fatalf("expected 0 batches for empty input, got %d", len(groups))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/search/ -run TestSplitIntoBatches -v`
Expected: compilation failure — `undefined: splitIntoBatches`.

- [ ] **Step 3: Add the helper and constants**

Append to `internal/search/rerank.go` (above `Reranker` type is fine; group with other private helpers):

```go
// Voyage rerank-2 limits (also valid for rerank-2.5/-lite variants per docs).
const (
	// maxBatchTokens is our safe ceiling under Voyage's 600k batch limit.
	// 8% headroom covers tokenizer-mismatch drift between our cl100k_base
	// estimate and Voyage's Qwen2 tokenizer.
	maxBatchTokens = 550_000

	// maxDocsPerCall is Voyage's hard limit on documents per rerank call.
	maxDocsPerCall = 1000

	// maxPairTokens is Voyage's per-pair (query + single doc) cap for
	// rerank-2 when truncation=true. We clamp per-doc contributions to
	// this when computing the batch budget, matching Voyage's server-side
	// truncation.
	maxPairTokens = 16_000
)

// splitIntoBatches greedily packs docs (referenced by index) into sub-batches
// whose total Voyage cost stays ≤ maxTokens and whose size stays ≤ maxDocs.
//
// Voyage's batch cost formula is:
//
//	query_tokens * N + sum(clamped_doc_tokens_i)
//
// where clamped_doc_tokens_i = min(doc_tokens_i, maxPairTokens - query_tokens).
// The clamp reflects Voyage's server-side per-pair truncation.
//
// Order is preserved: each returned slice contains a contiguous run of input
// indices. A document whose clamped cost alone exceeds maxTokens still gets a
// singleton batch (Voyage will truncate it server-side).
func splitIntoBatches(queryTokens int, docTokens []int, maxTokens, maxDocs int) [][]int {
	if len(docTokens) == 0 {
		return nil
	}

	perDocCap := maxPairTokens - queryTokens
	if perDocCap < 0 {
		perDocCap = 0
	}

	clamp := func(d int) int {
		if d > perDocCap {
			return perDocCap
		}
		return d
	}

	var groups [][]int
	var current []int
	currentCost := 0

	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			current = nil
			currentCost = 0
		}
	}

	for i, d := range docTokens {
		add := queryTokens + clamp(d)
		// If adding this doc would overflow, flush first.
		if len(current) > 0 && (currentCost+add > maxTokens || len(current) >= maxDocs) {
			flush()
		}
		current = append(current, i)
		currentCost += add
	}
	flush()
	return groups
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/search/ -run TestSplitIntoBatches -v`
Expected: all five `TestSplitIntoBatches_*` cases PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/search/rerank.go internal/search/rerank_test.go
git commit -m "feat: add bin-packing helper for voyage rerank batches

splitIntoBatches enforces Voyage's batch token formula
(query_tokens*N + sum(clamped_doc_tokens)) and per-call doc count cap.
Pure function; wired into Rerank in the next commit."
```

---

## Task 4: Wire bin-packing + parallel dispatch into `Rerank` (TDD)

**Files:**
- Modify: `internal/search/rerank.go` (replace `Rerank` body, add merge logic)
- Modify: `internal/search/rerank_test.go` (add fake backend + behavioral tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/search/rerank_test.go`:

```go
import (
	// add to existing imports if not already present:
	"strings"
	"sync"
	"sync/atomic"
	"github.com/austinfhunter/voyageai"
)

// fakeBackend records each Rerank call and returns scores keyed by doc content.
// Each doc string should look like "doc-<n>"; the fake assigns score 1/(n+1) so
// lower-numbered docs rank higher (deterministic, easy to assert on).
type fakeBackend struct {
	mu        sync.Mutex
	calls     int
	callDocs  [][]string
	failCallN int32 // if >0, the Nth call (1-indexed) returns an error
	counter   int32
}

func (f *fakeBackend) Rerank(query string, documents []string, model string, opts *voyageai.RerankRequestOpts) (*voyageai.RerankResponse, error) {
	n := atomic.AddInt32(&f.counter, 1)
	f.mu.Lock()
	f.calls++
	cpy := append([]string(nil), documents...)
	f.callDocs = append(f.callDocs, cpy)
	f.mu.Unlock()
	if f.failCallN > 0 && n == f.failCallN {
		return nil, fmt.Errorf("simulated voyage failure on call %d", n)
	}
	data := make([]voyageai.RerankObject, len(documents))
	for i, d := range documents {
		var num int
		_, _ = fmt.Sscanf(d, "doc-%d", &num)
		data[i] = voyageai.RerankObject{
			Index:          i,
			RelevanceScore: float32(1.0 / float64(num+1)),
		}
	}
	return &voyageai.RerankResponse{Data: data}, nil
}

func newTestReranker(b rerankBackend) *Reranker {
	return &Reranker{backend: b, model: "rerank-2"}
}

func TestRerank_SingleCallUnderBudget(t *testing.T) {
	docs := []string{"doc-0", "doc-1", "doc-2"}
	fb := &fakeBackend{}
	r := newTestReranker(fb)

	results, err := r.Rerank(context.Background(), "q", docs, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.calls != 1 {
		t.Fatalf("expected 1 backend call, got %d", fb.calls)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (topK=2), got %d", len(results))
	}
	if results[0].Index != 0 || results[1].Index != 1 {
		t.Fatalf("expected top results [0, 1], got [%d, %d]", results[0].Index, results[1].Index)
	}
}

func TestRerank_SplitsWhenOverBudget(t *testing.T) {
	// Build docs so the total cost forces a split. Each doc is ~1500 chars
	// ≈ ~450 cl100k tokens × 1.2 = ~540 voyage tokens. With 600 docs the
	// batch budget (550k) is exceeded → expect ≥2 sub-batches.
	const n = 800
	docs := make([]string, n)
	body := strings.Repeat("word ", 500) // ~500 words, ~2500 chars
	for i := range docs {
		docs[i] = fmt.Sprintf("doc-%d %s", i, body)
	}
	fb := &fakeBackend{}
	r := newTestReranker(fb)

	results, err := r.Rerank(context.Background(), "q", docs, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.calls < 2 {
		t.Fatalf("expected ≥2 backend calls due to budget split, got %d", fb.calls)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10 results (topK=10), got %d", len(results))
	}
	// Top results should be the lowest-numbered docs (fake assigns
	// score = 1/(n+1)). Just check the first one is doc index 0.
	if results[0].Index != 0 {
		t.Fatalf("expected top result Index=0, got %d", results[0].Index)
	}
	// Indices must be globally unique and within range.
	seen := make(map[int]bool)
	for _, r := range results {
		if r.Index < 0 || r.Index >= n {
			t.Fatalf("result Index %d out of range [0,%d)", r.Index, n)
		}
		if seen[r.Index] {
			t.Fatalf("duplicate global index %d in results", r.Index)
		}
		seen[r.Index] = true
	}
}

func TestRerank_IndexMappingAcrossBatches(t *testing.T) {
	// Force a split by passing a budget the helper would respect.
	// We can't reach the production constant directly from tests at this
	// granularity; instead use big docs to force ≥2 calls and assert that
	// the returned Index values reference the caller's slice (0..N-1),
	// not the sub-batch local indices.
	const n = 600
	docs := make([]string, n)
	body := strings.Repeat("payload ", 800) // ~6400 chars
	for i := range docs {
		docs[i] = fmt.Sprintf("doc-%d %s", i, body)
	}
	fb := &fakeBackend{}
	r := newTestReranker(fb)

	results, err := r.Rerank(context.Background(), "q", docs, n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.calls < 2 {
		t.Fatalf("expected ≥2 calls to exercise mapping, got %d", fb.calls)
	}
	if len(results) != n {
		t.Fatalf("expected %d results, got %d", n, len(results))
	}
	seen := make(map[int]bool)
	for _, rr := range results {
		if rr.Index < 0 || rr.Index >= n {
			t.Fatalf("Index %d outside global range [0,%d)", rr.Index, n)
		}
		if seen[rr.Index] {
			t.Fatalf("duplicate Index %d", rr.Index)
		}
		seen[rr.Index] = true
	}
	if len(seen) != n {
		t.Fatalf("expected every input index represented, got %d unique", len(seen))
	}
}

func TestRerank_BackendErrorPropagates(t *testing.T) {
	const n = 600
	docs := make([]string, n)
	body := strings.Repeat("payload ", 800)
	for i := range docs {
		docs[i] = fmt.Sprintf("doc-%d %s", i, body)
	}
	fb := &fakeBackend{failCallN: 1}
	r := newTestReranker(fb)

	_, err := r.Rerank(context.Background(), "q", docs, 10)
	if err == nil {
		t.Fatal("expected error from failed backend call, got nil")
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/search/ -run 'TestRerank_SingleCallUnderBudget|TestRerank_SplitsWhenOverBudget|TestRerank_IndexMappingAcrossBatches|TestRerank_BackendErrorPropagates' -v`
Expected: `TestRerank_SingleCallUnderBudget` likely passes already; the split-based tests FAIL (single call regardless of size, no merge logic).

- [ ] **Step 3: Replace `Rerank` with the sub-batching implementation**

In `internal/search/rerank.go`, replace the entire existing `Rerank` method with:

```go
// Rerank sends the query and documents to Voyage's rerank endpoint and returns
// the top-K results sorted by relevance score descending. Each RerankResult's
// Index references the caller's original `documents` slice.
//
// When the total batch would exceed Voyage's 600k token limit (or the 1000
// docs-per-call limit), the request is transparently split into sub-batches
// dispatched in parallel; per-batch results are then merged by score. The
// scoring is preserved because Voyage's reranker is a cross-encoder
// (per-pair scoring, comparable across calls).
func (r *Reranker) Rerank(ctx context.Context, query string, documents []string, topK int) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	queryTokens := pipeline.CountTokens(query)
	docTokens := make([]int, len(documents))
	for i, d := range documents {
		docTokens[i] = pipeline.CountTokens(d)
	}

	groups := splitIntoBatches(queryTokens, docTokens, maxBatchTokens, maxDocsPerCall)
	if len(groups) > 1 {
		total := 0
		for _, d := range docTokens {
			total += d
		}
		log.Printf("voyage rerank: split %d candidates into %d sub-batches (query=%d tokens, total docs=%d tokens)",
			len(documents), len(groups), queryTokens, total)
	}

	// Per-batch topK: ask Voyage for all results so we have everything to
	// merge across batches; we trim globally after merge.
	var (
		mu       sync.Mutex
		all      []RerankResult
	)

	g, gctx := errgroup.WithContext(ctx)
	for _, group := range groups {
		group := group // capture
		g.Go(func() error {
			subDocs := make([]string, len(group))
			for i, idx := range group {
				subDocs[i] = documents[idx]
			}
			resp, err := r.backend.Rerank(query, subDocs, r.model, &voyageai.RerankRequestOpts{})
			if err != nil {
				return fmt.Errorf("voyage rerank: %w", err)
			}
			// Re-check the context after a possibly-slow call.
			if err := gctx.Err(); err != nil {
				return err
			}
			local := make([]RerankResult, len(resp.Data))
			for i, obj := range resp.Data {
				// obj.Index is local to subDocs; map back to global.
				if obj.Index < 0 || obj.Index >= len(group) {
					return fmt.Errorf("voyage rerank: backend returned out-of-range index %d (sub-batch size %d)", obj.Index, len(group))
				}
				local[i] = RerankResult{
					Index: group[obj.Index],
					Score: float64(obj.RelevanceScore),
				}
			}
			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Score > all[j].Score
	})
	if topK > 0 && len(all) > topK {
		all = all[:topK]
	}
	return all, nil
}
```

Add the missing imports at the top of `internal/search/rerank.go`:

```go
import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/austinfhunter/voyageai"
	"golang.org/x/sync/errgroup"

	"mykb/internal/pipeline"
)
```

- [ ] **Step 4: Run all rerank tests**

Run: `go test ./internal/search/ -v`
Expected: all `TestRerank_*` and `TestSplitIntoBatches_*` cases PASS. Integration test stays skipped.

- [ ] **Step 5: Build & full test sweep**

Run: `just build && just test`
Expected: build succeeds; all packages pass.

- [ ] **Step 6: Lint**

Run: `just lint`
Expected: no findings. If lint flags unused `_ = ctx` or similar, address inline.

- [ ] **Step 7: Commit**

```bash
git add internal/search/rerank.go internal/search/rerank_test.go
git commit -m "fix: cap voyage rerank batch tokens by sub-batching

Previously Rerank forwarded the candidate slice unchanged to Voyage,
which fails when the batch exceeds 600k tokens (query*N + sum(doc)).
Now we compute the batch cost client-side, bin-pack into sub-batches
under a 550k-token budget (8% headroom), dispatch them in parallel
via errgroup, and merge results by score. Cross-encoder scores are
comparable across calls, so the merged top-K matches a single-call
ranking.

Fixes the bug in docs/bugs/voyage-rerank-600k-batch-overflow.md."
```

---

## Task 5: Manual end-to-end verification against deployed server

**Files:** none (verification only)

The bug repro requires the deployed `mykb-api`. After merge, the user (or
deploy automation) needs to redeploy. This task documents the verification.

- [ ] **Step 1: Rebuild and redeploy**

Run (after the commits above land on main and CI publishes the image):

```bash
just k8s-restart
```

Expected: `mykb-api` rollout restarts; pod becomes Ready.

- [ ] **Step 2: Rebuild the local CLI**

Run:

```bash
just cli && cp mykb ~/.local/bin/mykb
```

Expected: `~/.local/bin/mykb` updated.

- [ ] **Step 3: Run the original bug reproduction**

Run:

```bash
mykb query "Bazzite immutable Linux NVIDIA" --no-merge
```

Expected: results returned, no 600k-token error.

- [ ] **Step 4: Run the high-depth case**

Run:

```bash
mykb query "Bazzite immutable Linux NVIDIA" --no-merge --rerank-depth 1000
```

Expected: results returned; server logs include
`voyage rerank: split N candidates into K sub-batches ...`.

- [ ] **Step 5: Update the bug document**

In `docs/bugs/voyage-rerank-600k-batch-overflow.md`, append a "Resolution"
section linking to the fix commit and noting verification details. Commit:

```bash
git add docs/bugs/voyage-rerank-600k-batch-overflow.md
git commit -m "docs: mark voyage rerank batch overflow as fixed"
```

---

## Self-review notes

- Spec coverage: ✅ Approach (sub-batch and merge), token-counting helper export, constants, observability, fake-backend testing, files-changed list — all mapped to Tasks 1–4. Task 5 covers the spec's manual E2E test.
- Placeholders: none — every code step contains the exact code.
- Type consistency: `splitIntoBatches`, `rerankBackend`, `voyageBackend`, `maxBatchTokens`, `maxDocsPerCall`, `maxPairTokens` used consistently across tasks.
