package tui

import (
	"strings"
	"testing"
)

func TestRenderHeader(t *testing.T) {
	item := ResultItem{
		Title:      "Test Article",
		URL:        "https://example.com/article",
		ChunkIndex: 2,
		ChunkCount: 5,
	}

	result := renderHeader(item, 40, false)

	if result == "" {
		t.Fatal("renderHeader returned empty string")
	}
	if !strings.Contains(result, "Test Article") {
		t.Error("header should contain title")
	}
	if !strings.Contains(result, "https://example.com/article") {
		t.Error("header should contain URL")
	}
	if !strings.Contains(result, "3/5") {
		t.Error("header should contain chunk position")
	}
	if !strings.Contains(result, "─") {
		t.Error("header should contain divider")
	}
}

func TestFindMatches(t *testing.T) {
	content := "Hello world\nfoo bar\nHello again\nbaz"

	t.Run("basic", func(t *testing.T) {
		matches := findMatches(content, "Hello")
		if len(matches) != 2 {
			t.Errorf("expected 2 matches, got %d", len(matches))
		}
		if len(matches) >= 2 && (matches[0] != 0 || matches[1] != 2) {
			t.Errorf("unexpected match lines: %v", matches)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		matches := findMatches(content, "hello")
		if len(matches) != 2 {
			t.Errorf("expected 2 matches, got %d", len(matches))
		}
	})

	t.Run("empty query", func(t *testing.T) {
		matches := findMatches(content, "")
		if matches != nil {
			t.Errorf("expected nil for empty query, got %v", matches)
		}
	})
}

func TestHighlightSearch(t *testing.T) {
	t.Run("plain text", func(t *testing.T) {
		result := highlightSearch("hello world hello", "hello")
		// The highlighted text should still contain the search term
		stripped := ansiRegex.ReplaceAllString(result, "")
		if !strings.Contains(stripped, "hello") {
			t.Error("highlighted text should still contain the search term when stripped")
		}
		if strings.Count(stripped, "hello") != 2 {
			t.Errorf("expected 2 occurrences of 'hello', got %d", strings.Count(stripped, "hello"))
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		result := highlightSearch("Hello World", "hello")
		stripped := ansiRegex.ReplaceAllString(result, "")
		if !strings.Contains(stripped, "Hello") {
			t.Error("should preserve original case in highlighted output")
		}
	})

	t.Run("with ANSI", func(t *testing.T) {
		input := "\x1b[31mhello\x1b[0m world"
		result := highlightSearch(input, "hello")
		stripped := ansiRegex.ReplaceAllString(result, "")
		if !strings.Contains(stripped, "hello") {
			t.Error("should find and highlight through ANSI codes")
		}
	})

	t.Run("empty search", func(t *testing.T) {
		input := "hello world"
		result := highlightSearch(input, "")
		if result != input {
			t.Error("empty search should return input unchanged")
		}
	})

	t.Run("no match", func(t *testing.T) {
		input := "hello world"
		result := highlightSearch(input, "zzz")
		if result != input {
			t.Error("no match should return input unchanged")
		}
	})

	t.Run("multiple matches per line", func(t *testing.T) {
		result := highlightSearch("foo bar foo baz foo", "foo")
		stripped := ansiRegex.ReplaceAllString(result, "")
		if strings.Count(stripped, "foo") != 3 {
			t.Errorf("expected 3 occurrences of 'foo' in stripped result, got %d", strings.Count(stripped, "foo"))
		}
	})
}
