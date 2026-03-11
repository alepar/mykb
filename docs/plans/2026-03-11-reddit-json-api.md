# Reddit JSON API Ingestion — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Bypass crawl4ai for Reddit thread URLs and use Reddit's JSON API to capture post + comments with full reply trees.

**Architecture:** New file `internal/pipeline/reddit.go` handles Reddit detection, JSON fetching, comment selection, and markdown assembly. `crawl.go` checks for Reddit URLs first and delegates. No changes to chunking, embedding, or indexing.

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`, `strings`, `sort`). No new dependencies.

---

### Task 1: Reddit comment model and markdown rendering

**Files:**
- Create: `internal/pipeline/reddit.go`
- Test: `internal/pipeline/reddit_test.go`

**Step 1: Write tests for comment tree flattening, selection, and markdown rendering**

```go
package pipeline

import (
	"strings"
	"testing"
)

func TestFlattenComments(t *testing.T) {
	// Build a small tree: root -> reply -> nested reply
	root := redditComment{
		Author: "alice",
		Body:   "top level",
		Score:  10,
		Replies: []redditComment{
			{
				Author: "bob",
				Body:   "reply to alice",
				Score:  5,
				Replies: []redditComment{
					{Author: "carol", Body: "nested reply", Score: 20},
				},
			},
		},
	}

	flat := flattenComments([]redditComment{root})
	if len(flat) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(flat))
	}
}

func TestSelectComments_TokenBudget(t *testing.T) {
	// Create many comments; budget should limit selection.
	var comments []redditComment
	for i := 0; i < 100; i++ {
		comments = append(comments, redditComment{
			Author: "user",
			Body:   strings.Repeat("word ", 200), // ~200 tokens each
			Score:  100 - i,
		})
	}

	selected := selectComments(comments, 1000) // budget for ~5 comments
	if len(selected) >= 100 {
		t.Fatalf("expected budget to limit selection, got %d", len(selected))
	}
	if len(selected) == 0 {
		t.Fatal("expected at least one comment selected")
	}
}

func TestSelectComments_IncludesAncestors(t *testing.T) {
	// A high-score nested reply should pull in its low-score parent.
	root := redditComment{
		Author: "alice",
		Body:   "boring parent",
		Score:  0,
		Replies: []redditComment{
			{Author: "bob", Body: "amazing insight", Score: 100},
		},
	}

	selected := selectComments([]redditComment{root}, 20000)
	// Both alice (ancestor) and bob (selected) should be included.
	bodies := make(map[string]bool)
	for _, c := range selected {
		bodies[c.Body] = true
	}
	if !bodies["boring parent"] {
		t.Fatal("ancestor not included")
	}
	if !bodies["amazing insight"] {
		t.Fatal("selected comment not included")
	}
}

func TestRenderCommentsMarkdown(t *testing.T) {
	comments := []redditComment{
		{
			Author: "alice",
			Body:   "top level comment",
			Score:  10,
			Replies: []redditComment{
				{Author: "bob", Body: "reply to alice", Score: 5},
			},
		},
	}

	md := renderCommentsMarkdown(comments)
	if !strings.Contains(md, "**u/alice**") {
		t.Fatal("missing author")
	}
	if !strings.Contains(md, "(10 pts)") {
		t.Fatal("missing score")
	}
	if !strings.Contains(md, "> >") {
		t.Fatal("missing nested blockquote for reply")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/pipeline/ -run 'TestFlattenComments|TestSelectComments|TestRenderCommentsMarkdown' -v`
Expected: FAIL — types and functions not defined.

**Step 3: Implement the Reddit comment types and functions**

In `internal/pipeline/reddit.go`:

```go
package pipeline

import (
	"fmt"
	"sort"
	"strings"
)

// redditComment represents a single comment in the tree.
type redditComment struct {
	Author  string
	Body    string
	Score   int
	Replies []redditComment
	parent  *redditComment // set during flattening
}

// flattenComments recursively collects all comments into a flat slice,
// setting parent pointers for ancestor traversal.
func flattenComments(comments []redditComment) []*redditComment {
	var result []*redditComment
	var walk func(cs []redditComment, parent *redditComment)
	walk = func(cs []redditComment, parent *redditComment) {
		for i := range cs {
			cs[i].parent = parent
			result = append(result, &cs[i])
			walk(cs[i].Replies, &cs[i])
		}
	}
	walk(comments, nil)
	return result
}

// estimateTokens returns a rough token count (chars / 4).
func estimateTokens(s string) int {
	return len(s) / 4
}

// selectComments picks top comments by score within a token budget,
// including full ancestor chains for context.
func selectComments(tree []redditComment, tokenBudget int) []redditComment {
	flat := flattenComments(tree)

	// Sort by score descending.
	sort.Slice(flat, func(i, j int) bool {
		return flat[i].Score > flat[j].Score
	})

	// Greedily select by score, tracking included set.
	type commentID = *redditComment
	included := make(map[commentID]bool)
	usedTokens := 0

	for _, c := range flat {
		if included[c] {
			continue
		}
		tokens := estimateTokens(c.Body)
		// Also account for ancestors not yet included.
		var ancestors []*redditComment
		for p := c.parent; p != nil && !included[p]; p = p.parent {
			ancestors = append(ancestors, p)
			tokens += estimateTokens(p.Body)
		}
		if usedTokens+tokens > tokenBudget && usedTokens > 0 {
			continue // skip if over budget (but always include at least one)
		}
		included[c] = true
		for _, a := range ancestors {
			included[a] = true
		}
		usedTokens += tokens
	}

	// Rebuild tree with only included comments.
	return filterTree(tree, included)
}

// filterTree returns a copy of the tree containing only included comments.
func filterTree(tree []redditComment, included map[*redditComment]bool) []redditComment {
	// We need to match by pointer, so re-flatten to get pointers into the original tree.
	flat := flattenComments(tree)
	includedSet := make(map[*redditComment]bool)
	for _, c := range flat {
		if included[c] {
			includedSet[c] = true
		}
	}

	var rebuild func(cs []redditComment) []redditComment
	rebuild = func(cs []redditComment) []redditComment {
		var result []redditComment
		for i := range cs {
			if !includedSet[&cs[i]] {
				// Check if any descendants are included.
				filtered := rebuild(cs[i].Replies)
				if len(filtered) > 0 {
					// Include this node as an ancestor.
					cp := cs[i]
					cp.Replies = filtered
					cp.parent = nil
					result = append(result, cp)
				}
				continue
			}
			cp := cs[i]
			cp.Replies = rebuild(cs[i].Replies)
			cp.parent = nil
			result = append(result, cp)
		}
		return result
	}
	return rebuild(tree)
}

// renderCommentsMarkdown renders a comment tree as nested blockquote markdown.
func renderCommentsMarkdown(comments []redditComment) string {
	var sb strings.Builder
	var render func(cs []redditComment, depth int)
	render = func(cs []redditComment, depth int) {
		for i, c := range cs {
			prefix := strings.Repeat("> ", depth+1)
			sb.WriteString(fmt.Sprintf("%s**u/%s** (%d pts)\n", prefix, c.Author, c.Score))
			// Indent each line of the body.
			for _, line := range strings.Split(c.Body, "\n") {
				sb.WriteString(fmt.Sprintf("%s%s\n", prefix, line))
			}
			render(c.Replies, depth+1)
			if i < len(cs)-1 {
				sb.WriteString("\n")
			}
		}
	}
	render(comments, 0)
	return sb.String()
}
```

Note: The `selectComments` function has a subtle issue — `flattenComments` is called twice (once in `selectComments` and once in `filterTree`), and the second call creates new pointers. The implementation above needs the pointer-based inclusion set to work across both calls. A simpler approach: use a `selected` boolean field on the struct. Here is the corrected approach that avoids this — mark comments with an `included` field directly, or use a unique index. **The implementing engineer should use the test suite to verify correctness and fix any pointer issues.**

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/pipeline/ -run 'TestFlattenComments|TestSelectComments|TestRenderCommentsMarkdown' -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/pipeline/reddit.go internal/pipeline/reddit_test.go
git commit -m "feat: add Reddit comment selection and markdown rendering"
```

---

### Task 2: Reddit JSON API fetching and URL detection

**Files:**
- Modify: `internal/pipeline/reddit.go`
- Modify: `internal/pipeline/crawl.go`
- Test: `internal/pipeline/reddit_test.go`

**Step 1: Write test for URL detection**

Add to `reddit_test.go`:

```go
func TestIsRedditThread(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.reddit.com/r/Rag/comments/1rhpmqw/improved_retrieval_accuracy/", true},
		{"https://reddit.com/r/golang/comments/abc123/some_title/", true},
		{"https://old.reddit.com/r/Rag/comments/1rhpmqw/title/", true},
		{"https://www.reddit.com/r/Rag/", false},
		{"https://go.dev/blog/maps", false},
		{"https://github.com/D-Star-AI/dsRAG", false},
	}
	for _, tt := range tests {
		if got := isRedditThread(tt.url); got != tt.want {
			t.Errorf("isRedditThread(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestIsRedditThread -v`
Expected: FAIL

**Step 3: Implement URL detection and JSON fetching**

Add to `reddit.go`:

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
)

var redditThreadPattern = regexp.MustCompile(`^https?://(?:www\.|old\.)?reddit\.com/r/\w+/comments/\w+`)

// isRedditThread returns true if the URL is a Reddit thread.
func isRedditThread(url string) bool {
	return redditThreadPattern.MatchString(url)
}

// redditAPIResponse represents the two-element array from Reddit's JSON API.
// Element 0 is the post listing, element 1 is the comment listing.
type redditListing struct {
	Data struct {
		Children []redditThing `json:"children"`
	} `json:"data"`
}

type redditThing struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type redditPostData struct {
	Title    string `json:"title"`
	Selftext string `json:"selftext"`
	Author   string `json:"author"`
	Score    int    `json:"score"`
}

type redditCommentData struct {
	Author   string      `json:"author"`
	Body     string      `json:"body"`
	Score    int         `json:"score"`
	Replies  interface{} `json:"replies"` // string "" or listing object
}

// fetchRedditThread fetches a Reddit thread via the JSON API and returns
// the post and comment tree.
func fetchRedditThread(ctx context.Context, client *http.Client, threadURL string) (redditPostData, []redditComment, error) {
	// Normalize URL: strip trailing slash, append .json
	u := strings.TrimRight(threadURL, "/") + ".json"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return redditPostData{}, nil, err
	}
	req.Header.Set("User-Agent", "mykb/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return redditPostData{}, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return redditPostData{}, nil, fmt.Errorf("reddit API returned status %d", resp.StatusCode)
	}

	var listings [2]redditListing
	if err := json.NewDecoder(resp.Body).Decode(&listings); err != nil {
		return redditPostData{}, nil, fmt.Errorf("decode reddit JSON: %w", err)
	}

	// Parse post.
	if len(listings[0].Data.Children) == 0 {
		return redditPostData{}, nil, fmt.Errorf("no post data in reddit response")
	}
	var post redditPostData
	if err := json.Unmarshal(listings[0].Data.Children[0].Data, &post); err != nil {
		return redditPostData{}, nil, fmt.Errorf("decode post: %w", err)
	}

	// Parse comment tree.
	comments := parseCommentChildren(listings[1].Data.Children)

	return post, comments, nil
}

// parseCommentChildren converts Reddit API things into our comment tree.
func parseCommentChildren(things []redditThing) []redditComment {
	var comments []redditComment
	for _, thing := range things {
		if thing.Kind != "t1" {
			continue
		}
		var cd redditCommentData
		if err := json.Unmarshal(thing.Data, &cd); err != nil {
			continue
		}
		c := redditComment{
			Author: cd.Author,
			Body:   cd.Body,
			Score:  cd.Score,
		}
		// Parse nested replies. Reddit returns "" for no replies, or a listing object.
		if cd.Replies != nil {
			if repliesBytes, err := json.Marshal(cd.Replies); err == nil {
				var repliesListing redditListing
				if json.Unmarshal(repliesBytes, &repliesListing) == nil {
					c.Replies = parseCommentChildren(repliesListing.Data.Children)
				}
			}
		}
		comments = append(comments, c)
	}
	return comments
}
```

**Step 4: Implement the Crawl dispatch in `crawl.go`**

Add a Reddit check at the top of the `Crawl` method:

```go
func (c *Crawler) Crawl(ctx context.Context, url string) (CrawlResult, error) {
	if isRedditThread(url) {
		return c.crawlReddit(ctx, url)
	}
	// ... existing crawl4ai code ...
}
```

Add `crawlReddit` method to `crawl.go` (or `reddit.go` — either works since same package):

```go
const redditCommentTokenBudget = 20000

// crawlReddit fetches a Reddit thread via the JSON API and assembles
// the post + top comments into markdown.
func (c *Crawler) crawlReddit(ctx context.Context, threadURL string) (CrawlResult, error) {
	post, comments, err := fetchRedditThread(ctx, c.httpClient, threadURL)
	if err != nil {
		return CrawlResult{}, fmt.Errorf("fetch reddit thread: %w", err)
	}

	selected := selectComments(comments, redditCommentTokenBudget)

	// Sort top-level by score descending.
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].Score > selected[j].Score
	})

	var md strings.Builder
	md.WriteString("# ")
	md.WriteString(post.Title)
	md.WriteString("\n\n")
	if post.Selftext != "" {
		md.WriteString(post.Selftext)
		md.WriteString("\n\n")
	}
	if len(selected) > 0 {
		md.WriteString("## Comments\n\n")
		md.WriteString(renderCommentsMarkdown(selected))
	}

	return CrawlResult{
		Markdown: md.String(),
		Title:    post.Title,
	}, nil
}
```

**Step 5: Run tests**

Run: `go test ./internal/pipeline/ -run 'TestIsRedditThread|TestFlattenComments|TestSelectComments|TestRenderComments' -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/pipeline/reddit.go internal/pipeline/reddit_test.go internal/pipeline/crawl.go
git commit -m "feat: fetch Reddit threads via JSON API with comment tree"
```

---

### Task 3: Integration test

**Files:**
- Add to: `internal/pipeline/reddit_test.go`

**Step 1: Write integration test**

This test hits the live Reddit API. Tag it so it doesn't run in CI.

```go
func TestRedditIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	client := &http.Client{Timeout: 30 * time.Second}

	url := "https://www.reddit.com/r/Rag/comments/1rhpmqw/improved_retrieval_accuracy_from_50_to_91_on/"
	post, comments, err := fetchRedditThread(ctx, client, url)
	if err != nil {
		t.Fatalf("fetchRedditThread: %v", err)
	}

	// Post checks.
	if post.Title == "" {
		t.Error("post title is empty")
	}
	if post.Selftext == "" {
		t.Error("post selftext is empty")
	}
	if !strings.Contains(post.Title, "retrieval accuracy") {
		t.Errorf("unexpected title: %s", post.Title)
	}

	// Comments checks.
	if len(comments) == 0 {
		t.Fatal("no comments returned")
	}

	// Check that we got at least some replies (not just top-level).
	hasReplies := false
	for _, c := range comments {
		if len(c.Replies) > 0 {
			hasReplies = true
			break
		}
	}
	if !hasReplies {
		t.Error("no nested replies found in comment tree")
	}

	// Test full pipeline: select + render.
	selected := selectComments(comments, 20000)
	if len(selected) == 0 {
		t.Fatal("no comments selected")
	}

	md := renderCommentsMarkdown(selected)
	if !strings.Contains(md, "**u/") {
		t.Error("rendered markdown missing author attribution")
	}
	if !strings.Contains(md, "pts)") {
		t.Error("rendered markdown missing score")
	}

	// Test full markdown assembly matches expected structure.
	var full strings.Builder
	full.WriteString("# " + post.Title + "\n\n")
	full.WriteString(post.Selftext + "\n\n")
	full.WriteString("## Comments\n\n")
	full.WriteString(md)

	assembled := full.String()
	if !strings.Contains(assembled, "# Improved retrieval accuracy") {
		t.Error("assembled markdown missing post title heading")
	}
	if !strings.Contains(assembled, "## Comments") {
		t.Error("assembled markdown missing comments section")
	}

	t.Logf("Post: %s (%d chars selftext)", post.Title, len(post.Selftext))
	t.Logf("Comments: %d top-level, %d selected", len(comments), len(selected))
	t.Logf("Rendered markdown: %d chars", len(assembled))
}
```

**Step 2: Run integration test**

Run: `go test ./internal/pipeline/ -run TestRedditIntegration -v`
Expected: PASS with log output showing post title, comment counts, and markdown size.

**Step 3: Commit**

```bash
git add internal/pipeline/reddit_test.go
git commit -m "test: add Reddit JSON API integration test"
```

---

### Task 4: End-to-end test via CLI

**Step 1: Rebuild server and ingest a Reddit URL**

```bash
docker compose up -d --build mykb
# Wait a few seconds for startup
./mykb ingest "https://www.reddit.com/r/Rag/comments/1rhpmqw/improved_retrieval_accuracy_from_50_to_91_on/"
```

Expected: Completes with `done.`

**Step 2: Verify the stored markdown contains comments**

```bash
# Find the document ID
docker compose exec -T postgres psql -U mykb -d mykb -t -A \
  -c "SELECT id FROM documents WHERE url LIKE '%1rhpmqw%';"

# Check the markdown file for comment content (substitute the ID)
grep -c "u/" data/documents/<sharded-path>/<id>.md
```

Expected: Multiple matches showing comment author attributions.

**Step 3: Query and verify comments are searchable**

```bash
./mykb query "cross-encoder reranking" --no-merge
```

Expected: Results should include content from the Reddit comments (not just the post body).

**Step 4: Commit if any fixes were needed**

```bash
git commit -m "fix: <description of any fixes>"
```
