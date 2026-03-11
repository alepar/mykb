package pipeline

import (
	"strings"
	"testing"
)

func TestFlattenComments(t *testing.T) {
	tree := []redditComment{
		{
			Author: "alice",
			Body:   "top level",
			Score:  10,
			Replies: []redditComment{
				{
					Author: "bob",
					Body:   "reply to alice",
					Score:  5,
					Replies: []redditComment{
						{
							Author: "charlie",
							Body:   "nested reply",
							Score:  2,
						},
					},
				},
			},
		},
	}

	flat := flattenComments(tree)
	if len(flat) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(flat))
	}

	// Verify parent pointers
	if flat[0].parent != nil {
		t.Error("root comment should have nil parent")
	}

	// Find bob and charlie by author
	var bob, charlie *redditComment
	for _, c := range flat {
		switch c.Author {
		case "bob":
			bob = c
		case "charlie":
			charlie = c
		}
	}

	if bob == nil || charlie == nil {
		t.Fatal("could not find bob or charlie in flat list")
	}
	if bob.parent == nil || bob.parent.Author != "alice" {
		t.Error("bob's parent should be alice")
	}
	if charlie.parent == nil || charlie.parent.Author != "bob" {
		t.Error("charlie's parent should be bob")
	}
}

func TestSelectComments_TokenBudget(t *testing.T) {
	// Create 100 comments each with ~200 tokens of body text (800 chars).
	var tree []redditComment
	for i := 0; i < 100; i++ {
		tree = append(tree, redditComment{
			Author: "user",
			Body:   strings.Repeat("word ", 160), // 160*5=800 chars => 200 tokens
			Score:  100 - i,
		})
	}

	selected := selectComments(tree, 1000)
	if len(selected) >= 100 {
		t.Errorf("expected fewer than 100 comments with budget 1000, got %d", len(selected))
	}
	if len(selected) == 0 {
		t.Error("expected at least some comments to be selected")
	}
}

func TestSelectComments_IncludesAncestors(t *testing.T) {
	// A low-score parent with a high-score nested reply.
	// The parent should be included because the reply needs its ancestor chain.
	tree := []redditComment{
		{
			Author: "low_score_parent",
			Body:   "parent comment",
			Score:  1,
			Replies: []redditComment{
				{
					Author: "high_score_child",
					Body:   "great reply",
					Score:  999,
				},
			},
		},
		{
			Author: "medium_score",
			Body:   "another top level",
			Score:  50,
		},
	}

	selected := selectComments(tree, 10000)

	// Find the high-score child; its parent must also be present.
	foundParent := false
	foundChild := false
	var walk func(comments []redditComment)
	walk = func(comments []redditComment) {
		for _, c := range comments {
			switch c.Author {
			case "low_score_parent":
				foundParent = true
			case "high_score_child":
				foundChild = true
			}
			walk(c.Replies)
		}
	}
	walk(selected)

	if !foundChild {
		t.Error("high_score_child should be selected")
	}
	if !foundParent {
		t.Error("low_score_parent should be included as ancestor of high_score_child")
	}
}

func TestRenderCommentsMarkdown(t *testing.T) {
	comments := []redditComment{
		{
			Author: "alice",
			Body:   "Top level comment text...",
			Score:  10,
			Replies: []redditComment{
				{
					Author: "bob",
					Body:   "Reply to alice...",
					Score:  5,
				},
			},
		},
	}

	md := renderCommentsMarkdown(comments)

	// Check author attribution
	if !strings.Contains(md, "**u/alice**") {
		t.Error("expected author attribution for alice")
	}
	if !strings.Contains(md, "**u/bob**") {
		t.Error("expected author attribution for bob")
	}

	// Check score
	if !strings.Contains(md, "(10 pts)") {
		t.Error("expected score for alice")
	}
	if !strings.Contains(md, "(5 pts)") {
		t.Error("expected score for bob")
	}

	// Check nested blockquotes — bob's lines should start with "> >"
	lines := strings.Split(md, "\n")
	foundNested := false
	for _, line := range lines {
		if strings.HasPrefix(line, "> > ") && strings.Contains(line, "bob") {
			foundNested = true
			break
		}
	}
	if !foundNested {
		t.Errorf("expected nested blockquote for bob's comment, got:\n%s", md)
	}
}
