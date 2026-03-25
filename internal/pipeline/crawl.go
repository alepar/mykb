package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	Markdown    string // fit (filtered) markdown, or raw if fit is empty
	RawMarkdown string // unfiltered raw markdown
	Title       string
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
	URLs          []string             `json:"urls"`
	Priority      int                  `json:"priority"`
	CrawlerConfig *crawlCrawlerConfig  `json:"crawler_config,omitempty"`
}

type crawlCrawlerConfig struct {
	Type   string                  `json:"type"`
	Params crawlCrawlerConfigParams `json:"params"`
}

type crawlCrawlerConfigParams struct {
	MarkdownGenerator *crawlMarkdownGenerator `json:"markdown_generator,omitempty"`
}

type crawlMarkdownGenerator struct {
	Type   string                       `json:"type"`
	Params crawlMarkdownGeneratorParams `json:"params"`
}

type crawlMarkdownGeneratorParams struct {
	ContentFilter *crawlContentFilter `json:"content_filter,omitempty"`
}

type crawlContentFilter struct {
	Type   string                  `json:"type"`
	Params crawlContentFilterParams `json:"params"`
}

type crawlContentFilterParams struct {
	Threshold float64 `json:"threshold"`
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
	FitMarkdown string `json:"fit_markdown"`
}

type crawlMetadata struct {
	Title string `json:"title"`
}

const (
	crawlMaxRetries = 5
	crawlBaseDelay  = 4 * time.Second
)

func (c *Crawler) Crawl(ctx context.Context, url string) (CrawlResult, error) {
	var lastErr error
	for attempt := 0; attempt <= crawlMaxRetries; attempt++ {
		if attempt > 0 {
			delay := crawlBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("crawl: retry %d/%d for %s after %v", attempt, crawlMaxRetries, url, delay)
			select {
			case <-ctx.Done():
				return CrawlResult{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		result, err := c.crawlOnce(ctx, url)
		if err != nil {
			lastErr = err
			log.Printf("crawl: attempt %d failed for %s: %v", attempt, url, err)
			continue
		}
		return result, nil
	}
	return CrawlResult{}, fmt.Errorf("crawl failed after %d retries: %w", crawlMaxRetries, lastErr)
}

// CrawlWithHTML converts pre-fetched HTML to markdown via Crawl4AI's raw: protocol.
// This bypasses URL fetching — the HTML is sent directly for markdown conversion.
// Retries with backoff on transport failures.
func (c *Crawler) CrawlWithHTML(ctx context.Context, url string, html string) (CrawlResult, error) {
	var lastErr error
	for attempt := 0; attempt <= crawlMaxRetries; attempt++ {
		if attempt > 0 {
			delay := crawlBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("crawl-html: retry %d/%d for %s after %v", attempt, crawlMaxRetries, url, delay)
			select {
			case <-ctx.Done():
				return CrawlResult{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		result, err := c.crawlRawHTML(ctx, url, html)
		if err != nil {
			lastErr = err
			log.Printf("crawl-html: attempt %d failed for %s: %v", attempt, url, err)
			continue
		}
		return result, nil
	}
	return CrawlResult{}, fmt.Errorf("crawl-html failed after %d retries: %w", crawlMaxRetries, lastErr)
}

// crawlRawHTML sends HTML content to Crawl4AI using the raw: prefix.
func (c *Crawler) crawlRawHTML(ctx context.Context, url string, html string) (CrawlResult, error) {
	body, err := json.Marshal(crawlRequest{
		URLs:     []string{"raw:" + html},
		Priority: 10,
		CrawlerConfig: &crawlCrawlerConfig{
			Type: "CrawlerRunConfig",
			Params: crawlCrawlerConfigParams{
				MarkdownGenerator: &crawlMarkdownGenerator{
					Type: "DefaultMarkdownGenerator",
					Params: crawlMarkdownGeneratorParams{
						ContentFilter: &crawlContentFilter{
							Type:   "PruningContentFilter",
							Params: crawlContentFilterParams{Threshold: 0.48},
						},
					},
				},
			},
		},
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

	rawMarkdown := ""
	fitMarkdown := ""
	if result.Markdown != nil {
		rawMarkdown = result.Markdown.RawMarkdown
		fitMarkdown = result.Markdown.FitMarkdown
	}

	markdown := fitMarkdown
	if markdown == "" {
		markdown = rawMarkdown
	}

	// Title from metadata (unlikely with raw: mode) or first heading.
	title := ""
	if result.Metadata != nil && result.Metadata.Title != "" {
		title = result.Metadata.Title
	} else {
		title = extractTitle(markdown)
	}

	return CrawlResult{
		Markdown:    markdown,
		RawMarkdown: rawMarkdown,
		Title:       title,
	}, nil
}

// IsRedditURL returns true if the URL should be crawled via the Reddit path.
func IsRedditURL(url string) bool {
	return isRedditThread(url)
}

// CrawlBatch sends multiple URLs in a single /crawl POST request.
// Returns successful results keyed by URL and per-URL errors keyed by URL.
// Retry with backoff applies to transport-level failures only (HTTP errors, timeouts).
// Per-URL failures in the response are NOT retried — they're returned as errors.
func (c *Crawler) CrawlBatch(ctx context.Context, urls []string) (map[string]CrawlResult, map[string]error) {
	if len(urls) == 0 {
		return nil, nil
	}

	// Longer timeout for batch: 2 min per URL, capped at 10 min.
	timeout := time.Duration(len(urls)) * 2 * time.Minute
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	batchClient := &http.Client{Timeout: timeout}

	body, err := json.Marshal(crawlRequest{
		URLs:     urls,
		Priority: 10,
		CrawlerConfig: &crawlCrawlerConfig{
			Type: "CrawlerRunConfig",
			Params: crawlCrawlerConfigParams{
				MarkdownGenerator: &crawlMarkdownGenerator{
					Type: "DefaultMarkdownGenerator",
					Params: crawlMarkdownGeneratorParams{
						ContentFilter: &crawlContentFilter{
							Type:   "PruningContentFilter",
							Params: crawlContentFilterParams{Threshold: 0.48},
						},
					},
				},
			},
		},
	})
	if err != nil {
		errs := make(map[string]error, len(urls))
		for _, u := range urls {
			errs[u] = err
		}
		return nil, errs
	}

	var lastErr error
	for attempt := 0; attempt <= crawlMaxRetries; attempt++ {
		if attempt > 0 {
			delay := crawlBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("crawl-batch: retry %d/%d for %d urls after %v", attempt, crawlMaxRetries, len(urls), delay)
			select {
			case <-ctx.Done():
				errs := make(map[string]error, len(urls))
				for _, u := range urls {
					errs[u] = ctx.Err()
				}
				return nil, errs
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/crawl", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := batchClient.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("crawl-batch: attempt %d failed: %v", attempt, err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("crawl4ai returned status %d: %s", resp.StatusCode, string(respBody))
			log.Printf("crawl-batch: attempt %d failed: %v", attempt, lastErr)
			continue
		}

		var cr crawlResponse
		if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
			resp.Body.Close()
			lastErr = fmt.Errorf("decode crawl response: %w", err)
			log.Printf("crawl-batch: attempt %d failed: %v", attempt, lastErr)
			continue
		}
		resp.Body.Close()

		return c.parseBatchResults(urls, &cr)
	}

	errs := make(map[string]error, len(urls))
	for _, u := range urls {
		errs[u] = fmt.Errorf("crawl-batch failed after %d retries: %w", crawlMaxRetries, lastErr)
	}
	return nil, errs
}

// parseBatchResults converts a crawlResponse into per-URL results and errors.
func (c *Crawler) parseBatchResults(urls []string, cr *crawlResponse) (map[string]CrawlResult, map[string]error) {
	results := make(map[string]CrawlResult)
	errs := make(map[string]error)

	responseByURL := make(map[string]crawlResult, len(cr.Results))
	for _, r := range cr.Results {
		responseByURL[r.URL] = r
	}

	for _, url := range urls {
		r, ok := responseByURL[url]
		if !ok {
			errs[url] = fmt.Errorf("crawl4ai returned no result for URL")
			continue
		}
		if !r.Success {
			errs[url] = fmt.Errorf("crawl4ai failed: %s", r.Error)
			continue
		}

		rawMarkdown := ""
		fitMarkdown := ""
		if r.Markdown != nil {
			rawMarkdown = r.Markdown.RawMarkdown
			fitMarkdown = r.Markdown.FitMarkdown
		}

		markdown := fitMarkdown
		if markdown == "" {
			markdown = rawMarkdown
		}

		title := ""
		if r.Metadata != nil && r.Metadata.Title != "" {
			title = r.Metadata.Title
		} else {
			title = extractTitle(markdown)
		}

		results[url] = CrawlResult{
			Markdown:    markdown,
			RawMarkdown: rawMarkdown,
			Title:       title,
		}
	}

	return results, errs
}

// crawlOnce fetches the given URL via the Crawl4AI container and returns
// the page content as markdown. It uses PruningContentFilter to produce
// fit_markdown (filtered content without navigation/boilerplate).
func (c *Crawler) crawlOnce(ctx context.Context, url string) (CrawlResult, error) {
	if isRedditThread(url) {
		return c.crawlReddit(ctx, url)
	}

	body, err := json.Marshal(crawlRequest{
		URLs:     []string{url},
		Priority: 10,
		CrawlerConfig: &crawlCrawlerConfig{
			Type: "CrawlerRunConfig",
			Params: crawlCrawlerConfigParams{
				MarkdownGenerator: &crawlMarkdownGenerator{
					Type: "DefaultMarkdownGenerator",
					Params: crawlMarkdownGeneratorParams{
						ContentFilter: &crawlContentFilter{
							Type:   "PruningContentFilter",
							Params: crawlContentFilterParams{Threshold: 0.48},
						},
					},
				},
			},
		},
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

	rawMarkdown := ""
	fitMarkdown := ""
	if result.Markdown != nil {
		rawMarkdown = result.Markdown.RawMarkdown
		fitMarkdown = result.Markdown.FitMarkdown
	}

	// Use fit markdown as the primary content; fall back to raw.
	markdown := fitMarkdown
	if markdown == "" {
		markdown = rawMarkdown
	}

	title := ""
	if result.Metadata != nil && result.Metadata.Title != "" {
		title = result.Metadata.Title
	} else {
		title = extractTitle(markdown)
	}

	return CrawlResult{
		Markdown:    markdown,
		RawMarkdown: rawMarkdown,
		Title:       title,
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
