# wiki-research examples

Three worked traces. Read these to anchor the playbook in concrete behavior.

## Example 1 — mykb-only shortcut

**User**: "What's the embedding dimension we use?"

**Phase 2** — `mykb query` returns:
- tier 1: `wiki://main/synthesis/which-embedding-model.md` (question: "Which embedding model do we use and why?")
- tier 2: `wiki://main/concepts/embeddings.md`
- tier 3: a few raw https:// articles about Voyage AI

**Phase 3** — read `synthesis/which-embedding-model.md`:
- `question:` directly covers the user's ask.
- `answered_at: 2026-04-25`, today is `2026-05-01`. `stale_after_days = 180`. Fresh.

Quote the short answer:
> We use Voyage `voyage-context-3` (2048-dim).

Tell the user:
> Already answered in `synthesis/which-embedding-model.md` (answered 2026-04-25). Short answer: voyage-context-3, 2048-dim. Want to use that, or run fresh research?

**User**: "Use that."

Done. No writes. No deep-research call.

## Example 2 — Full pipeline with one contradiction

**User**: "Research the current state of int8 quantization in vector databases for the wiki."

**Phase 2** — `mykb query` returns:
- tier 2: `wiki://main/concepts/quantization.md` (claims: "int8 typically loses 1-2% recall vs full-precision")
- tier 3: a Qdrant blog post from 2024
- no synthesis page on this question

**Phase 3** — no synthesis match → continue.

**Phase 4** — seed brief includes the wiki claim about 1-2% recall loss, the Qdrant post, and a flagged gap: "no coverage of recent benchmarks (2025+)".

**Phase 5** — call `Skill(deep-research, ...)`. Standard mode. Returns a report citing 12 sources.

**Phase 6** — cross-check. The deep-research report says recent Qdrant benchmarks show **3-5% recall loss** on long-context embeddings, citing two 2025 papers and a Qdrant 2025 blog. This **contradicts** the wiki concept page.

Stop. Tell the user:

> Contradiction: `concepts/quantization.md` says "int8 typically loses 1-2% recall". Deep research found 3-5% on long-context (2048-dim) embeddings, per Qdrant's 2025 benchmark and two 2025 papers. The wiki claim was likely correct for shorter embeddings but is too narrow for our voyage-context-3 (2048-dim) setup.
>
> Resolve as: trust web (update the concept page), trust mykb, mark uncertain, or something else?

**User**: "Trust web. Update the concept."

**Phase 7a** — show diff for `concepts/quantization.md`:
```diff
-int8 typically loses 1-2% recall vs full-precision.
+int8 loses 1-2% recall on short embeddings; 3-5% on long-context (2048-dim) embeddings,
+per [Qdrant 2025 benchmarks](https://...) and two 2025 papers.
```
User approves. Write, `mykb wiki ingest concepts/quantization.md`, log entry.

**Phase 7b** — no new entities/concepts warranted (the topic is already covered by `quantization.md` and existing `qdrant.md` entity).

**Phase 7c** — write `synthesis/int8-quantization-state-2026.md`:
```yaml
---
type: synthesis
question: "What's the current state of int8 quantization in vector databases?"
answered_at: 2026-05-01
superseded_by: null
sources:
  - wiki://main/concepts/quantization.md
  - wiki://main/entities/qdrant.md
  - https://qdrant.tech/articles/...-2025
  - https://arxiv.org/abs/...
  - https://arxiv.org/abs/...
---

# What's the current state of int8 quantization in vector databases?

int8 quantization is mature in the major vector DBs (Qdrant, Milvus, pgvector).
Recall loss is small on short embeddings (1–2%) and modest on long-context
embeddings (3–5%). Latency wins are 2–4× depending on workload.

## Reasoning

[[Qdrant]]'s int8 mode applies symmetric quantization with per-collection min/max
calibration. As of 2025, the common finding is that long-context embeddings
(see [[embeddings]] and [[concepts/quantization]]) take a larger recall hit ...

## Open questions

- We have not benchmarked our specific voyage-context-3 + Qdrant int8 combination.
  Worth doing if we ever see recall complaints in production.
```

Write, ingest, log.

**Phase 7d** — Log.md gets two lines (one for the concept update, one for the synthesis add).

**Phase 7e** — summary: 1 page updated, 1 page added, 5 sources cited (2 wiki, 3 web), 1 open question flagged.

## Example 3 — Full pipeline introducing new entities

**User**: "Research RankZephyr for the wiki."

**Phase 2** — `mykb query` returns nothing for RankZephyr specifically. tier 2: `wiki://main/concepts/reranking.md` mentions "rerank-2.5" and "Cohere Rerank" but not RankZephyr.

**Phase 3** — no synthesis match → continue.

**Phase 4** — seed brief: question is "what is RankZephyr"; existing wiki context is the reranking concept page; gap: no entity for RankZephyr.

**Phase 5** — deep-research returns a report citing the original paper, the GitHub repo, and several blog comparisons.

**Phase 6** — no contradictions; everything is extension over the existing wiki. Continue.

**Phase 7a** — update `concepts/reranking.md`: add RankZephyr to the list of known rerankers, with a [[entities/rankzephyr]] wikilink (forward-link, will resolve once the entity is created in 7b). User approves.

**Phase 7b** — propose new pages:
- `entities/rankzephyr.md` (kind: model). Show draft to user. User approves: write, ingest, log.
- `entities/castorini.md` (kind: org) — the lab that built RankZephyr. User approves a slim stub.

**Phase 7c** — write `synthesis/what-is-rankzephyr.md` with `sources:` listing both new entity pages, the updated concept page, and 4 web URLs.

**Phase 7d-e** — log lines for each write; summary lists 3 adds, 1 update, 7 sources.
