package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

var redditThreadPattern = regexp.MustCompile(`^https?://(?:www\.|old\.)?reddit\.com/r/\w+/comments/\w+`)

func isRedditThread(url string) bool {
	return redditThreadPattern.MatchString(url)
}

// redditPostData holds the fields we extract from a Reddit post (t3).
type redditPostData struct {
	Title    string `json:"title"`
	Selftext string `json:"selftext"`
	Author   string `json:"author"`
	Score    int    `json:"score"`
}

// redditListing represents the top-level Reddit JSON API listing structure.
type redditListing struct {
	Data struct {
		Children []redditListingChild `json:"children"`
	} `json:"data"`
}

type redditListingChild struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// redditCommentData is the raw JSON shape for a comment (t1).
type redditCommentData struct {
	Author  string          `json:"author"`
	Body    string          `json:"body"`
	Score   int             `json:"score"`
	Replies json.RawMessage `json:"replies"`
}

// fetchRedditThread fetches a Reddit thread via the JSON API and returns
// the post data and the comment tree.
func fetchRedditThread(ctx context.Context, client *http.Client, threadURL string) (redditPostData, []redditComment, error) {
	// Build JSON API URL: strip trailing slash, append .json
	u := strings.TrimRight(threadURL, "/") + ".json"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return redditPostData{}, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "mykb/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return redditPostData{}, nil, fmt.Errorf("fetch reddit thread: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return redditPostData{}, nil, fmt.Errorf("reddit returned status %d: %s", resp.StatusCode, string(body))
	}

	var listings [2]redditListing
	if err := json.NewDecoder(resp.Body).Decode(&listings); err != nil {
		return redditPostData{}, nil, fmt.Errorf("decode reddit JSON: %w", err)
	}

	// Extract post from listings[0].data.children[0].data
	if len(listings[0].Data.Children) == 0 {
		return redditPostData{}, nil, fmt.Errorf("no post found in reddit response")
	}
	var post redditPostData
	if err := json.Unmarshal(listings[0].Data.Children[0].Data, &post); err != nil {
		return redditPostData{}, nil, fmt.Errorf("decode post data: %w", err)
	}

	// Parse comment tree from listings[1].data.children
	comments := parseCommentChildren(listings[1].Data.Children)

	return post, comments, nil
}

// parseCommentChildren converts redditListingChild entries into redditComment tree nodes.
func parseCommentChildren(children []redditListingChild) []redditComment {
	var comments []redditComment
	for _, child := range children {
		if child.Kind != "t1" {
			continue // skip "more" stubs and non-comment entries
		}
		var cd redditCommentData
		if err := json.Unmarshal(child.Data, &cd); err != nil {
			continue
		}
		c := redditComment{
			Author:  cd.Author,
			Body:    cd.Body,
			Score:   cd.Score,
			Replies: parseReplies(cd.Replies),
		}
		comments = append(comments, c)
	}
	return comments
}

// parseReplies handles the polymorphic replies field: it's either "" (empty string)
// or a listing object containing nested comments.
func parseReplies(raw json.RawMessage) []redditComment {
	if len(raw) == 0 {
		return nil
	}
	// Check if it's a string (empty replies are encoded as "")
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return nil
	}
	// Otherwise it's a listing object
	var listing redditListing
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil
	}
	return parseCommentChildren(listing.Data.Children)
}

const redditCommentTokenBudget = 20000

// crawlReddit fetches a Reddit thread via the JSON API and returns a CrawlResult
// with the post and top comments rendered as markdown.
func (c *Crawler) crawlReddit(ctx context.Context, threadURL string) (CrawlResult, error) {
	post, comments, err := fetchRedditThread(ctx, c.httpClient, threadURL)
	if err != nil {
		return CrawlResult{}, err
	}

	// Select top comments within token budget.
	selected := selectComments(comments, redditCommentTokenBudget)

	// Sort top-level selected comments by score descending.
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].Score > selected[j].Score
	})

	// Assemble markdown.
	var sb strings.Builder
	sb.WriteString("# ")
	sb.WriteString(post.Title)
	sb.WriteString("\n\n")
	if post.Selftext != "" {
		sb.WriteString(post.Selftext)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Comments\n\n")
	sb.WriteString(renderCommentsMarkdown(selected))

	return CrawlResult{
		Markdown: sb.String(),
		Title:    post.Title,
	}, nil
}

type redditComment struct {
	Author  string
	Body    string
	Score   int
	Replies []redditComment
	parent  *redditComment // set during flattening
}

// flattenComments recursively flattens a comment tree into a flat slice,
// setting parent pointers for ancestor traversal.
func flattenComments(comments []redditComment) []*redditComment {
	var result []*redditComment
	var walk func(comments []redditComment, parent *redditComment)
	walk = func(comments []redditComment, parent *redditComment) {
		for i := range comments {
			comments[i].parent = parent
			result = append(result, &comments[i])
			walk(comments[i].Replies, &comments[i])
		}
	}
	walk(comments, nil)
	return result
}

// selectComments picks top comments by score within a token budget,
// including full ancestor chains. It returns a new tree containing only
// selected and ancestor comments.
func selectComments(tree []redditComment, tokenBudget int) []redditComment {
	// Deep copy so we can set parent pointers without mutating the caller's data.
	// filterTree below uses pointer identity from this same copy to check inclusion.
	copied := deepCopyComments(tree)
	flat := flattenComments(copied)

	// Sort by score descending.
	sort.Slice(flat, func(i, j int) bool {
		return flat[i].Score > flat[j].Score
	})

	// Greedily select top comments until budget is exhausted.
	included := make(map[*redditComment]bool)
	usedTokens := 0

	for _, c := range flat {
		tokens := estimateTokens(c.Body)
		if usedTokens+tokens > tokenBudget {
			continue
		}
		included[c] = true
		usedTokens += tokens

		// Walk up ancestor chain and include all ancestors.
		for p := c.parent; p != nil; p = p.parent {
			if included[p] {
				break // already included, ancestors above are too
			}
			included[p] = true
			// Ancestors don't count against budget — they're structural.
		}
	}

	// Reassemble a filtered tree from the original structure.
	return filterTree(copied, included)
}

// deepCopyComments creates a deep copy of a comment tree.
func deepCopyComments(comments []redditComment) []redditComment {
	if comments == nil {
		return nil
	}
	result := make([]redditComment, len(comments))
	for i, c := range comments {
		result[i] = redditComment{
			Author:  c.Author,
			Body:    c.Body,
			Score:   c.Score,
			Replies: deepCopyComments(c.Replies),
		}
	}
	return result
}

// filterTree rebuilds a comment tree containing only included comments.
func filterTree(comments []redditComment, included map[*redditComment]bool) []redditComment {
	var result []redditComment
	for i := range comments {
		if !included[&comments[i]] {
			continue
		}
		filtered := redditComment{
			Author:  comments[i].Author,
			Body:    comments[i].Body,
			Score:   comments[i].Score,
			Replies: filterTree(comments[i].Replies, included),
		}
		result = append(result, filtered)
	}
	return result
}

// renderCommentsMarkdown renders a comment tree as nested blockquote markdown.
func renderCommentsMarkdown(comments []redditComment) string {
	var sb strings.Builder
	renderCommentsAt(&sb, comments, 1)
	return sb.String()
}

func renderCommentsAt(sb *strings.Builder, comments []redditComment, depth int) {
	prefix := strings.Repeat("> ", depth)
	for i, c := range comments {
		if i > 0 || depth > 1 {
			// Blank quoted line to separate from previous content.
			if i > 0 {
				sb.WriteString(strings.Repeat("> ", depth-1))
				sb.WriteString("\n")
			}
		}
		sb.WriteString(fmt.Sprintf("%s**u/%s** (%d pts)\n", prefix, c.Author, c.Score))

		// Render body lines with the same prefix.
		bodyLines := strings.Split(strings.TrimSpace(c.Body), "\n")
		for _, line := range bodyLines {
			sb.WriteString(fmt.Sprintf("%s%s\n", prefix, line))
		}

		if len(c.Replies) > 0 {
			sb.WriteString(prefix + "\n")
			renderCommentsAt(sb, c.Replies, depth+1)
		}
	}
}
