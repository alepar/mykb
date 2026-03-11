package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// crawlRequest is the POST body for /crawl.
type crawlRequest struct {
	URLs     []string `json:"urls"`
	Priority int      `json:"priority"`
}

// crawlResponse is the synchronous response from Crawl4AI v0.5.x.
type crawlResponse struct {
	Success bool          `json:"success"`
	Results []crawlResult `json:"results"`
}

type crawlResult struct {
	URL      string        `json:"url"`
	Success  bool          `json:"success"`
	Markdown *crawlMarkdown `json:"markdown"`
	Metadata *crawlMetadata `json:"metadata"`
	Error    string        `json:"error_message"`
}

type crawlMarkdown struct {
	RawMarkdown string `json:"raw_markdown"`
}

type crawlMetadata struct {
	Title string `json:"title"`
}

// Crawl fetches the given URL via the Crawl4AI container and returns
// the page content as markdown.
func (c *Crawler) Crawl(ctx context.Context, url string) (CrawlResult, error) {
	body, err := json.Marshal(crawlRequest{
		URLs:     []string{url},
		Priority: 10,
	})
	if err != nil {
		return CrawlResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/crawl", bytes.NewReader(body))
	if err != nil {
		return CrawlResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return CrawlResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return CrawlResult{}, fmt.Errorf("crawl4ai returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var cr crawlResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return CrawlResult{}, fmt.Errorf("decode crawl response: %w", err)
	}

	if !cr.Success || len(cr.Results) == 0 {
		return CrawlResult{}, fmt.Errorf("crawl4ai returned no results")
	}

	result := cr.Results[0]
	if !result.Success {
		return CrawlResult{}, fmt.Errorf("crawl4ai failed: %s", result.Error)
	}

	markdown := ""
	if result.Markdown != nil {
		markdown = result.Markdown.RawMarkdown
	}

	title := ""
	if result.Metadata != nil && result.Metadata.Title != "" {
		title = result.Metadata.Title
	} else {
		title = extractTitle(markdown)
	}

	return CrawlResult{
		Markdown: markdown,
		Title:    title,
	}, nil
}

// extractTitle returns the text of the first "# " heading in the markdown,
// or an empty string if none is found.
func extractTitle(markdown string) string {
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, "# ") && !strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}
