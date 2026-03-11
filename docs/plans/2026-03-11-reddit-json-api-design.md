# Reddit Ingestion via JSON API

## Problem

Crawl4ai cannot capture Reddit comments because Reddit loads them via JavaScript. Only the original post body is captured, missing the most valuable content in discussion threads.

## Solution

Bypass crawl4ai for Reddit thread URLs. Use Reddit's public JSON API (`{url}.json`) which returns the full post and comment tree with no authentication required.

## Detection

In `crawl.go`, if the URL matches `reddit.com/r/*/comments/*`, use the Reddit JSON API path instead of calling crawl4ai.

## Data Source

HTTP GET to `{thread_url}.json` with `User-Agent: mykb/1.0`. Returns a two-element JSON array: `[post_listing, comments_listing]`.

- `post_listing.data.children[0].data` contains: `title`, `selftext` (markdown), `author`, `score`
- `comments_listing.data.children[]` contains the comment tree, each with: `body` (markdown), `author`, `score`, `replies` (recursive)

## Comment Selection Algorithm

1. Recursively flatten all comments from the tree into a list.
2. Sort by score descending.
3. Greedily select top comments until a ~20K token budget is exhausted.
4. For each selected comment, walk up the ancestor chain to the root and include all ancestors.
5. Deduplicate ancestors that appear in multiple chains.
6. Reassemble into a tree structure for rendering.

Top-level comments sorted by score. Replies within each thread in natural order.

## Markdown Output Format

```markdown
# Post Title

Post body markdown...

## Comments

> **u/author1** (15 pts)
> Top-level comment text...
>
> > **u/author2** (8 pts)
> > Reply text...
```

## Integration with Pipeline

`CrawlResult` returns the assembled markdown as `Markdown` (primary content). `RawMarkdown` is empty since crawl4ai is not involved. Title comes from the post JSON.

The rest of the pipeline (chunking, embedding, indexing) processes the markdown normally with no changes.

## Testing

Integration test that fetches a known Reddit thread via the JSON API, assembles markdown, and verifies:

- Post title and body are present.
- Comments are included with author and score.
- Reply tree structure is preserved (nested blockquotes).
- Token budget cap is respected.
- Output structure matches what would be expected from the HTML version (post title, body, comment content).
