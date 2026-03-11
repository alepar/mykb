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
