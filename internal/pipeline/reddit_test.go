package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsRedditThread(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.reddit.com/r/Rag/comments/1rhpmqw/title/", true},
		{"https://reddit.com/r/golang/comments/abc123/title/", true},
		{"https://old.reddit.com/r/Rag/comments/1rhpmqw/title/", true},
		{"https://www.reddit.com/r/Rag/", false},
		{"https://go.dev/blog/maps", false},
	}
	for _, tt := range tests {
		got := isRedditThread(tt.url)
		if got != tt.want {
			t.Errorf("isRedditThread(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestFetchRedditThread(t *testing.T) {
	// Build a fake Reddit JSON API response: two-element array of listings.
	postData := map[string]interface{}{
		"title":    "Test Post Title",
		"selftext": "This is the post body.",
		"author":   "op_user",
		"score":    42,
	}
	replyCommentData := map[string]interface{}{
		"author":  "replier",
		"body":    "A reply",
		"score":   7,
		"replies": "",
	}
	replyListing := map[string]interface{}{
		"data": map[string]interface{}{
			"children": []map[string]interface{}{
				{"kind": "t1", "data": replyCommentData},
			},
		},
	}
	topCommentData := map[string]interface{}{
		"author":  "commenter",
		"body":    "Top level comment",
		"score":   15,
		"replies": replyListing,
	}

	listings := [2]interface{}{
		map[string]interface{}{
			"data": map[string]interface{}{
				"children": []map[string]interface{}{
					{"kind": "t3", "data": postData},
				},
			},
		},
		map[string]interface{}{
			"data": map[string]interface{}{
				"children": []map[string]interface{}{
					{"kind": "t1", "data": topCommentData},
				},
			},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/r/test/comments/abc123.json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if ua := r.Header.Get("User-Agent"); ua != "mykb/1.0" {
			t.Errorf("User-Agent = %q, want %q", ua, "mykb/1.0")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(listings)
	}))
	defer ts.Close()

	post, comments, err := fetchRedditThread(context.Background(), ts.Client(), ts.URL+"/r/test/comments/abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if post.Title != "Test Post Title" {
		t.Errorf("post title = %q, want %q", post.Title, "Test Post Title")
	}
	if post.Author != "op_user" {
		t.Errorf("post author = %q, want %q", post.Author, "op_user")
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 top-level comment, got %d", len(comments))
	}
	if comments[0].Author != "commenter" {
		t.Errorf("comment author = %q, want %q", comments[0].Author, "commenter")
	}
	if len(comments[0].Replies) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(comments[0].Replies))
	}
	if comments[0].Replies[0].Author != "replier" {
		t.Errorf("reply author = %q, want %q", comments[0].Replies[0].Author, "replier")
	}
}

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

func TestRedditIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	client := &http.Client{}
	threadURL := "https://www.reddit.com/r/Rag/comments/1rhpmqw/improved_retrieval_accuracy_from_50_to_91_on/"

	// 1. Fetch the thread
	post, comments, err := fetchRedditThread(ctx, client, threadURL)
	if err != nil {
		if strings.Contains(err.Error(), "status 403") || strings.Contains(err.Error(), "status 429") {
			t.Skip("skipping: Reddit blocked request (likely TLS fingerprint or rate limit)")
		}
		t.Fatalf("fetchRedditThread: %v", err)
	}

	// Verify post has non-empty title containing "retrieval accuracy" and non-empty selftext
	if !strings.Contains(strings.ToLower(post.Title), "retrieval accuracy") {
		t.Errorf("expected title to contain 'retrieval accuracy', got %q", post.Title)
	}
	if post.Selftext == "" {
		t.Error("expected non-empty selftext")
	}

	// 2. Comments are returned
	if len(comments) == 0 {
		t.Fatal("expected at least one comment")
	}

	// 3. At least one comment has nested replies
	hasNested := false
	var checkReplies func([]redditComment)
	checkReplies = func(cs []redditComment) {
		for _, c := range cs {
			if len(c.Replies) > 0 {
				hasNested = true
				return
			}
			checkReplies(c.Replies)
		}
	}
	checkReplies(comments)
	if !hasNested {
		t.Error("expected at least one comment with nested replies")
	}

	// 4. selectComments with 20000 budget returns comments
	selected := selectComments(comments, 20000)
	if len(selected) == 0 {
		t.Error("selectComments(20000) returned no comments")
	}

	// 5. renderCommentsMarkdown output contains author attribution and score
	md := renderCommentsMarkdown(selected)
	if !strings.Contains(md, "**u/") {
		t.Error("rendered markdown missing author attribution (**u/)")
	}
	if !strings.Contains(md, "pts)") {
		t.Error("rendered markdown missing score (pts)")
	}

	// 6. Full assembled markdown contains expected headings
	var sb strings.Builder
	sb.WriteString("# ")
	sb.WriteString(post.Title)
	sb.WriteString("\n\n")
	if post.Selftext != "" {
		sb.WriteString(post.Selftext)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Comments\n\n")
	sb.WriteString(md)
	assembled := sb.String()

	if !strings.Contains(assembled, "# Improved retrieval accuracy") {
		t.Error("assembled markdown missing '# Improved retrieval accuracy'")
	}
	if !strings.Contains(assembled, "## Comments") {
		t.Error("assembled markdown missing '## Comments'")
	}

	// 7. Log stats
	t.Logf("Post title: %s", post.Title)
	t.Logf("Selftext length: %d", len(post.Selftext))
	t.Logf("Comment count: %d", len(comments))
	t.Logf("Selected count: %d", len(selected))
	t.Logf("Assembled markdown length: %d", len(assembled))
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
