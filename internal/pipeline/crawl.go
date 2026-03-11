package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Crawler interacts with a Crawl4AI container to convert web pages to markdown.
type Crawler struct {
	baseURL    string
	httpClient *http.Client
}

// CrawlResult holds the output of a successful crawl.
type CrawlResult struct {
	Markdown string
	Title    string
}

// NewCrawler creates a Crawler pointed at the given Crawl4AI base URL.
func NewCrawler(baseURL string) *Crawler {
	return &Crawler{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
	}
}

// TODO: The Crawl4AI API format may vary by version. Verify the request/response
// shapes against the actual running container (unclecode/crawl4ai:latest on port 11235).

// crawlRequest is the POST body for /crawl.
type crawlRequest struct {
	URLs     []string `json:"urls"`
	Priority int      `json:"priority"`
}

// crawlResponse is the response from POST /crawl.
type crawlResponse struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// taskResponse is the response from GET /task/{id}.
type taskResponse struct {
	Status string      `json:"status"`
	Result *taskResult `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// taskResult holds the crawl output inside a completed task.
type taskResult struct {
	Markdown string `json:"markdown"`
}

// Crawl fetches the given URL via the Crawl4AI container and returns
// the page content as markdown.
func (c *Crawler) Crawl(ctx context.Context, url string) (CrawlResult, error) {
	taskID, err := c.submitCrawl(ctx, url)
	if err != nil {
		return CrawlResult{}, fmt.Errorf("submit crawl: %w", err)
	}

	result, err := c.pollTask(ctx, taskID)
	if err != nil {
		return CrawlResult{}, fmt.Errorf("poll task: %w", err)
	}

	return CrawlResult{
		Markdown: result.Markdown,
		Title:    extractTitle(result.Markdown),
	}, nil
}

// submitCrawl sends a crawl request and returns the task ID.
func (c *Crawler) submitCrawl(ctx context.Context, url string) (string, error) {
	body, err := json.Marshal(crawlRequest{
		URLs:     []string{url},
		Priority: 10,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/crawl", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("crawl4ai returned status %d", resp.StatusCode)
	}

	var cr crawlResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", fmt.Errorf("decode crawl response: %w", err)
	}
	if cr.TaskID == "" {
		return "", fmt.Errorf("crawl4ai returned empty task_id")
	}
	return cr.TaskID, nil
}

// pollTask polls the task endpoint until the task completes, fails, or the
// context is cancelled.
func (c *Crawler) pollTask(ctx context.Context, taskID string) (*taskResult, error) {
	pollInterval := 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		tr, err := c.getTask(ctx, taskID)
		if err != nil {
			return nil, err
		}

		switch tr.Status {
		case "completed":
			if tr.Result == nil {
				return nil, fmt.Errorf("task completed but result is nil")
			}
			return tr.Result, nil
		case "failed":
			msg := tr.Error
			if msg == "" {
				msg = "unknown error"
			}
			return nil, fmt.Errorf("crawl4ai task failed: %s", msg)
		}

		// Still pending — wait before polling again.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// getTask fetches the current state of a task.
func (c *Crawler) getTask(ctx context.Context, taskID string) (*taskResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/task/"+taskID, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("task endpoint returned status %d", resp.StatusCode)
	}

	var tr taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode task response: %w", err)
	}
	return &tr, nil
}

// extractTitle returns the text of the first "# " heading in the markdown,
// or an empty string if none is found.
func extractTitle(markdown string) string {
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "#"))
		}
	}
	return ""
}
