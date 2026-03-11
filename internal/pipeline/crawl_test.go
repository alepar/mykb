package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCrawl_SuccessfulCrawl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/crawl":
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
				TaskID: "task-123",
				Status: "pending",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/task/task-123":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(taskResponse{
				Status: "completed",
				Result: &taskResult{
					Markdown: "# My Page\n\nSome content here.",
				},
			})

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
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

func TestCrawl_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response that never completes
		if r.Method == http.MethodPost && r.URL.Path == "/crawl" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(crawlResponse{
				TaskID: "task-slow",
				Status: "pending",
			})
			return
		}
		// Task always pending
		if r.URL.Path == "/task/task-slow" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(taskResponse{
				Status: "pending",
			})
			return
		}
	}))
	defer server.Close()

	c := NewCrawler(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.Crawl(ctx, "https://example.com")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestExtractTitle_FromHeading(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		want     string
	}{
		{
			name:     "standard h1",
			markdown: "# Hello World\n\nBody text.",
			want:     "Hello World",
		},
		{
			name:     "h1 with extra spaces",
			markdown: "#   Spaced Title  \n\nBody.",
			want:     "Spaced Title",
		},
		{
			name:     "no heading",
			markdown: "Just some text without a heading.",
			want:     "",
		},
		{
			name:     "h2 only",
			markdown: "## Not a title\n\nBody.",
			want:     "",
		},
		{
			name:     "empty markdown",
			markdown: "",
			want:     "",
		},
		{
			name:     "heading not on first line",
			markdown: "Some preamble\n# Actual Title\n\nBody.",
			want:     "Actual Title",
		},
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

func TestCrawl_TaskFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/crawl" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(crawlResponse{
				TaskID: "task-fail",
				Status: "pending",
			})
			return
		}
		if r.URL.Path == "/task/task-fail" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(taskResponse{
				Status: "failed",
				Error:  "page not reachable",
			})
			return
		}
	}))
	defer server.Close()

	c := NewCrawler(server.URL)
	_, err := c.Crawl(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected error for failed task, got nil")
	}
}
