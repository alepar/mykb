package tui

import (
	"strings"
	"testing"
)

func TestResultItem_Domain(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"full url", "https://example.com/path", "example.com"},
		{"with port", "https://example.com:8080/path", "example.com:8080"},
		{"no scheme", "not-a-url", "not-a-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ResultItem{URL: tt.url}
			if got := r.Domain(); got != tt.want {
				t.Errorf("Domain() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResultItem_ChunkPosition(t *testing.T) {
	tests := []struct {
		name       string
		item       ResultItem
		want       string
	}{
		{"range", ResultItem{ChunkIndex: 3, ChunkIndexEnd: 5, ChunkCount: 8}, "3-5/8"},
		{"single", ResultItem{ChunkIndex: 4, ChunkIndexEnd: 0, ChunkCount: 8}, "4/8"},
		{"no count", ResultItem{ChunkIndex: 1, ChunkIndexEnd: 0, ChunkCount: 0}, ""},
		{"negative count", ResultItem{ChunkIndex: 1, ChunkIndexEnd: 0, ChunkCount: -1}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.ChunkPosition(); got != tt.want {
				t.Errorf("ChunkPosition() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterItems(t *testing.T) {
	items := []ResultItem{
		{Title: "Go Tutorial", URL: "https://golang.org/doc"},
		{Title: "Rust Book", URL: "https://doc.rust-lang.org/book"},
		{Title: "Python Guide", URL: "https://docs.python.org/guide"},
	}

	tests := []struct {
		name   string
		filter string
		want   int
	}{
		{"empty filter returns all", "", 3},
		{"domain match", "golang", 1},
		{"title match", "rust", 1},
		{"case insensitive", "PYTHON", 1},
		{"no match", "javascript", 0},
		{"partial domain", "org", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterItems(items, tt.filter)
			if len(got) != tt.want {
				t.Errorf("filterItems(%q) returned %d items, want %d", tt.filter, len(got), tt.want)
			}
		})
	}
}

func TestRenderSidebarEntry(t *testing.T) {
	item := ResultItem{
		Rank:  1,
		Score: 0.95,
		Title: "Test Title",
		URL:   "https://example.com/test",
	}

	result := renderSidebarEntry(item, 28, false)

	if result == "" {
		t.Fatal("renderSidebarEntry returned empty string")
	}
	if !strings.Contains(result, "#1") {
		t.Error("entry should contain rank")
	}
	if !strings.Contains(result, "0.95") {
		t.Error("entry should contain score")
	}
	if !strings.Contains(result, "example.com") {
		t.Error("entry should contain domain")
	}
}
