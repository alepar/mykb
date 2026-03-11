package pipeline

import (
	"fmt"
	"sort"
	"strings"
)

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
	// Deep copy the tree so we can safely set parent pointers and mutate.
	copied := deepCopyComments(tree)

	// Flatten to get parent pointers and a flat list.
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
