package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCrawl_SuccessfulCrawl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/crawl" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req crawlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.URLs) == 0 {
			http.Error(w, "no urls", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(crawlResponse{
			Success: true,
			Results: []crawlResult{{
				URL:     req.URLs[0],
				Success: true,
				Markdown: &crawlMarkdown{
					RawMarkdown: "# My Page\n\nSome content here.",
				},
				Metadata: &crawlMetadata{
					Title: "My Page",
				},
			}},
		})
	}))
	defer server.Close()

	c := NewCrawler(server.URL)
	result, err := c.Crawl(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Markdown != "# My Page\n\nSome content here." {
		t.Errorf("unexpected markdown: %q", result.Markdown)
	}
	if result.Title != "My Page" {
		t.Errorf("expected title %q, got %q", "My Page", result.Title)
	}
}

func TestCrawl_HTTPErrorFromCrawl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := NewCrawler(server.URL)
	_, err := c.Crawl(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCrawl_FailedResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(crawlResponse{
			Success: true,
			Results: []crawlResult{{
				URL:     "https://example.com",
				Success: false,
				Error:   "page not reachable",
			}},
		})
	}))
	defer server.Close()

	c := NewCrawler(server.URL)
	_, err := c.Crawl(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected error for failed crawl, got nil")
	}
}

func TestCrawl_TitleFallbackToMarkdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(crawlResponse{
			Success: true,
			Results: []crawlResult{{
				URL:     "https://example.com",
				Success: true,
				Markdown: &crawlMarkdown{
					RawMarkdown: "# Markdown Title\n\nBody.",
				},
			}},
		})
	}))
	defer server.Close()

	c := NewCrawler(server.URL)
	result, err := c.Crawl(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Title != "Markdown Title" {
		t.Errorf("expected title %q, got %q", "Markdown Title", result.Title)
	}
}

func TestExtractTitle_FromHeading(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		want     string
	}{
		{"standard h1", "# Hello World\n\nBody text.", "Hello World"},
		{"h1 with extra spaces", "#   Spaced Title  \n\nBody.", "Spaced Title"},
		{"no heading", "Just some text without a heading.", ""},
		{"h2 only", "## Not a title\n\nBody.", ""},
		{"empty markdown", "", ""},
		{"heading not on first line", "Some preamble\n# Actual Title\n\nBody.", "Actual Title"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTitle(tt.markdown)
			if got != tt.want {
				t.Errorf("extractTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}
