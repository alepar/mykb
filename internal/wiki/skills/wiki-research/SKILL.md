---
name: wiki-research
description: Use when the user asks to research a topic in the wiki, answer a question requiring sources, or fill a gap in vault knowledge — runs mykb-first retrieval, optional seeded web deep-research via the deep-research submodule, contradiction reconciliation, and wiki page updates
---

# wiki-research

Disciplined research loop for an mykb-backed wiki vault. Search local first, fall back to the web, cross-check, then write structured wiki pages with citations.

## Trust hierarchy

When weighing evidence:

1. **Synthesis pages** (`wiki://<name>/synthesis/...`) — validated answers, highest trust.
2. **Entity / concept pages** (`wiki://<name>/entities|concepts/...`) — curated, second.
3. **Raw articles** (`https://...` already in mykb) — hand-picked, third.
4. **Fresh web research** — useful for filling gaps and freshness checks, but always cross-checked against the above.

Never silent-edit an existing page. Every write requires explicit user approval. Synthesis pages are write-once: never edit; supersede.

## Workflow

The full phase-by-phase procedure lives in `playbook.md`. Read it before starting. High-level:

1. Receive query (clarify if vague — at most one question).
2. Run `mykb query` and bucket results by URL scheme into the trust tiers above.
3. **Coverage check**: skip web research only if a non-stale synthesis page directly answers the question. Otherwise continue.
4. Build a seed brief for `deep-research` containing the question, mykb evidence, and known gaps.
5. Invoke the `deep-research` skill (Standard mode by default) with the seed brief.
6. Cross-check findings against mykb. **Stop and ask the user on any contradiction.**
7. Update existing entity/concept pages (with diff approval), propose new ones (with approval), write a synthesis page citing all sources, append to `Log.md`.

## Hard rules

- After every file write in the vault, run `mykb wiki ingest <relative-path>` so the search index sees the change.
- Synthesis pages have `question:`, `answered_at:`, `superseded_by:` (initially `null`), and a complete `sources:` list of every URL cited in the body.
- Wikilinks: `[[name]]` for vault-internal references, full URLs for external citations.
- Append a one-line entry to `Log.md` for each page written or updated: `YYYY-MM-DD HH:MM <verb> <type> <path> [from sources …]`.
- If `.claude/skills/deep-research/SKILL.md` is missing, the submodule is not populated. Tell the user to run `git submodule update --init --recursive` from the vault root and stop.

## Examples

See `examples.md` for worked traces of three cases: mykb-only shortcut, full pipeline with one contradiction, and full pipeline introducing new entities.
