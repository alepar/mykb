# wiki-research playbook

The seven phases of a research run. Follow in order. Do not skip phases unless explicitly noted.

## Phase 0 — Preflight

Verify the deep-research submodule is populated:

```
ls .claude/skills/deep-research/SKILL.md
```

If missing, stop and tell the user:
> The `deep-research` submodule is not populated. From the vault root, run:
> ```
> git submodule update --init --recursive
> ```

Locate the vault root by walking up from cwd for `mykb-wiki.toml`. Read `mykb-wiki.toml` for `name` (used to interpret `wiki://` URLs) and `stale_after_days` (default 180; used in phase 3).

## Phase 1 — Receive query

If the user's request is unambiguous (a clear question or topic), proceed.

If vague ("research X" with no scope, or a topic that could mean several things), ask **at most one** clarifying question. Otherwise just commit to a reasonable interpretation and proceed — the user can correct later.

## Phase 2 — mykb-first retrieval

Run:

```
mykb query "<query>" --no-merge
```

Use `--no-merge` so individual chunks are returned (better for tier bucketing). Output is plain because Claude is non-TTY: each result includes the URL on its own line.

Bucket results by URL scheme:

| Tier | URL pattern | Trust |
|------|-------------|-------|
| 1 | `wiki://<name>/synthesis/...` | highest |
| 2 | `wiki://<name>/entities/...` or `wiki://<name>/concepts/...` | high |
| 3 | `https://...` | medium |

For tier 1 and tier 2 results, read the **full file** from disk (the URL `wiki://<name>/<path>` maps to `<vault-root>/<path>`). Read frontmatter and body. For tier 3, the chunk text in the search result is enough at this stage — you can read full pages later if a claim depends on context not in the chunk.

If `mykb query` returns no results, treat the query as a green-field topic; skip directly to phase 4.

## Phase 3 — Coverage check

The bar for skipping web research is **high**: only skip when a tier-1 synthesis page exists whose `question:` directly matches the user's query and is fresh.

**Direct match**: the synthesis page's `question:` field, read literally, answers the same thing the user is asking. Paraphrasing is fine; semantic equivalence is the standard.

**Freshness**: `(today - answered_at) <= stale_after_days` from `mykb-wiki.toml`. If stale, do NOT skip — re-run the research.

If a direct, fresh synthesis exists:

1. Quote the page's short answer (top of body).
2. Cite the page (`wiki://<name>/synthesis/<file>.md`) and any sources from its `sources:` frontmatter.
3. Ask the user: "This is already answered in `<page>`. Want me to use that, or run fresh research anyway?"
4. If the user accepts the existing answer, you are done. Do not write anything new.
5. If the user wants fresh research, continue to phase 4.

Otherwise, continue.

## Phase 4 — Seed brief

Compose a brief for the deep-research skill. Keep it under ~600 words. Structure:

```
QUESTION:
<the user's question, verbatim>

EXISTING WIKI EVIDENCE:
- [tier 1 page title] (wiki://...): <one-line summary + key claim or quote>
- [tier 2 page title] (wiki://...): <one-line summary>
... (only the most relevant 3–6 items)

EXISTING WEB SOURCES IN MYKB:
- <https://...> — <one-line relevance note>
...

OPEN QUESTIONS / KNOWN GAPS:
- <thing the wiki doesn't cover or where confidence is low>
- <claim that needs verification>

INSTRUCTIONS FOR DEEP-RESEARCH:
- Use the wiki evidence as a starting point, not as ground truth.
- Pay particular attention to <gaps>.
- Flag any source that contradicts the wiki claims above.
```

This brief becomes the prompt to the deep-research skill.

## Phase 5 — Invoke deep-research

Default mode: Standard. Override only if the user said "quick research", "deep research", or "ultradeep research" in their original ask.

Invoke via the `Skill` tool:

```
Skill(deep-research, "<seed brief from phase 4>")
```

The deep-research skill writes a full report to `~/Documents/<Topic>_Research_<Date>/`. Capture the markdown report path from its output. You will use the report findings, not the HTML/PDF artifacts.

If the deep-research skill fails (missing API keys, search-cli not installed, etc.), surface the error to the user verbatim and stop.

## Phase 6 — Cross-check & contradictions

Read the deep-research markdown report. For each major claim in the report, classify it against mykb evidence:

- **Agreement**: web finding matches an existing wiki claim. Cite both in the synthesis.
- **Extension**: web finding adds detail not previously in mykb. Capture for new pages or page updates.
- **Contradiction**: web finding disagrees with a wiki claim. **Stop and surface to the user before any writes.**

For each contradiction, present:
- The wiki claim (with the page path and the relevant quote).
- The web claim (with the source URL and quote).
- A short read of which seems more credible and why.

Ask the user how to resolve each one. Outcomes per item:

- **Trust mykb** — drop the web claim from the synthesis.
- **Trust web** — supersede the affected synthesis page (write a new one, set old `superseded_by:`) or update the affected entity/concept page (with diff approval; phase 7).
- **Mark uncertain** — capture in synthesis under `## Open questions`; on the affected concept page, lower `confidence:` to `low` and note the disagreement.
- **Supersede** — only valid for synthesis pages; never directly edit them.

Apply each resolution in phase 7. Do not proceed until every contradiction has a resolution.

## Phase 7 — Write/update wiki pages

Strict order:

### 7a. Update existing entity/concept pages

For each entity/concept page touched by the research:

1. Read the current file.
2. Compose the proposed edit (typically: refine description, add a property, link a new related concept, refresh `date_updated`).
3. Show the unified diff to the user.
4. On approval, write the file and update `date_updated` to today.
5. Run:
   ```
   mykb wiki ingest <relative-path>
   ```
6. Append to `Log.md`:
   ```
   YYYY-MM-DD HH:MM update <type> <path> [from sources <url>, <url>]
   ```

### 7b. Propose new entity/concept pages

For major new things surfaced (a new tool, dataset, person, technique), draft a page using the matching template under `.templates/`:

- New thing with a proper noun → `entities/<slug>.md` from `.templates/entity.md`.
- New abstraction or technique → `concepts/<slug>.md` from `.templates/concept.md`.
- Unsure → prefer concept.

Show each draft to the user. On approval, write + ingest + log (same as 7a but verb is `add`).

### 7c. Write the synthesis page

File: `synthesis/<slug>.md`, where `<slug>` is a short-kebab-case form of the question (or a noun-phrase summary).

Frontmatter:

```yaml
---
type: synthesis
question: "<the user's question, verbatim>"
answered_at: YYYY-MM-DD
superseded_by: null
sources:
  - wiki://<name>/<path>           # for each wiki page cited
  - https://...                    # for each web source cited
---
```

Body structure:

```markdown
# <The question>

<Short answer in 1–3 sentences. State the bottom line directly.>

## Reasoning

<Detailed reasoning. Use [[wikilinks]] to entities/concepts. Cite web sources inline as [n] with a footnote section, or with the URL in parentheses on first reference.>

## Open questions

<Anything not resolved. Include items where the user chose "mark uncertain" in phase 6.>

## Detailed report

<Optional. Path to the deep-research artifact under ~/Documents/... if the user wants the long-form linked.>
```

Write, ingest, log (verb: `add`).

### 7d. Append to `Log.md`

One line per page written or updated, in the order written. Format:

```
YYYY-MM-DD HH:MM <verb> <type> <relative-path> [from sources <url>, ...]
```

Where `<verb>` is `add` or `update`, `<type>` is `entity` / `concept` / `synthesis`. Sources list is the URLs that drove this particular write.

### 7e. Final summary

Tell the user:
- Files written (paths).
- Sources cited count (wiki / web).
- Any unresolved items still flagged in `## Open questions` of the synthesis.

## Failure modes

- **mykb server down**: `mykb query` returns a connection error. Tell the user; do not proceed without retrieval.
- **deep-research submodule empty**: caught in phase 0.
- **deep-research returns nothing useful**: tell the user; offer to retry with a different mode (Quick/Deep/UltraDeep) or to write a synthesis page based on mykb alone.
- **User rejects every diff**: respect that — do not write a synthesis page either. The research output is conversational only.
