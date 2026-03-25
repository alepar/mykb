# Firefox Extension HTML Ingest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Firefox extension send rendered page HTML alongside the URL so Crawl4AI converts it to markdown without re-fetching, bypassing bot protection.

**Architecture:** Extension captures `outerHTML` via content script injection, sends it with the URL to `/api/ingest`. Server writes HTML as a prefetch file. Worker's `doCrawl` checks for prefetch file and uses `CrawlWithHTML` (which sends `raw:<html>` to Crawl4AI) instead of fetching the URL.

**Tech Stack:** Firefox WebExtension (Manifest V2), Go, Crawl4AI `raw:` protocol

**Spec:** `docs/superpowers/specs/2026-03-25-extension-html-ingest-design.md`

---

### Task 1: Add prefetch HTML methods to FilesystemStore

**Files:**
- Modify: `internal/storage/filesystem.go`

- [ ] **Step 1: Add prefetch HTML methods**

Add these four methods after the existing `DeleteDocumentFiles` method. They follow the same sharding pattern as `WriteDocument`:

```go
// WritePrefetchHTML writes pre-fetched HTML content for a document.
// Used when the browser extension sends rendered HTML alongside the URL.
func (fs *FilesystemStore) WritePrefetchHTML(id string, html []byte) error {
	dir := fs.docDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create doc dir: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, id+".prefetch.html"), html, 0o644)
}

// ReadPrefetchHTML reads pre-fetched HTML for a document.
func (fs *FilesystemStore) ReadPrefetchHTML(id string) ([]byte, error) {
	return os.ReadFile(filepath.Join(fs.docDir(id), id+".prefetch.html"))
}

// HasPrefetchHTML returns true if a prefetch HTML file exists for a document.
func (fs *FilesystemStore) HasPrefetchHTML(id string) bool {
	_, err := os.Stat(filepath.Join(fs.docDir(id), id+".prefetch.html"))
	return err == nil
}

// DeletePrefetchHTML removes the prefetch HTML file (best-effort).
func (fs *FilesystemStore) DeletePrefetchHTML(id string) {
	_ = os.Remove(filepath.Join(fs.docDir(id), id+".prefetch.html"))
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add internal/storage/filesystem.go
git commit -m "feat: add prefetch HTML storage methods for browser-submitted content"
```

---

### Task 2: Add CrawlWithHTML method to Crawler

**Files:**
- Modify: `internal/pipeline/crawl.go`

- [ ] **Step 1: Add CrawlWithHTML method**

Add after the existing `Crawl` method. This sends raw HTML to Crawl4AI using the `raw:` protocol prefix instead of fetching a URL:

```go
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
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/crawl.go
git commit -m "feat: add CrawlWithHTML method using Crawl4AI raw: protocol"
```

---

### Task 3: Update worker to use prefetch HTML

**Files:**
- Modify: `internal/worker/worker.go`

- [ ] **Step 1: Update doCrawl to check for prefetch HTML**

In `doCrawl` (around line 231), after setting status to CRAWLING, check for prefetch HTML before calling the crawler:

Replace:
```go
result, err := w.crawler.Crawl(ctx, doc.URL)
```

With:
```go
var result pipeline.CrawlResult
var err error
if html, readErr := w.fs.ReadPrefetchHTML(doc.ID); readErr == nil {
	log.Printf("worker: using prefetch HTML for %s (%d bytes)", doc.URL, len(html))
	result, err = w.crawler.CrawlWithHTML(ctx, doc.URL, string(html))
	if err == nil {
		w.fs.DeletePrefetchHTML(doc.ID)
	}
} else {
	result, err = w.crawler.Crawl(ctx, doc.URL)
}
```

- [ ] **Step 2: Update processBatch to handle prefetch HTML docs separately**

In `processBatch`, after separating Reddit URLs from regular URLs (around line 580), add a third category for prefetch HTML docs:

After the Reddit/regular split loop, add:
```go
var prefetchDocs []batchDoc
var regularDocsFiltered []batchDoc
for _, bd := range regularDocs {
	if w.fs.HasPrefetchHTML(bd.doc.ID) {
		prefetchDocs = append(prefetchDocs, bd)
	} else {
		regularDocsFiltered = append(regularDocsFiltered, bd)
	}
}
regularDocs = regularDocsFiltered
```

Then in the crawl section, add goroutines for prefetch docs alongside Reddit docs:
```go
// Individual crawl for prefetch HTML docs.
for _, bd := range prefetchDocs {
	crawlWg.Add(1)
	go func(bd batchDoc) {
		defer crawlWg.Done()
		html, err := w.fs.ReadPrefetchHTML(bd.doc.ID)
		if err != nil {
			crawlErrors[bd.doc.URL] = err
			return
		}
		result, err := w.crawler.CrawlWithHTML(ctx, bd.doc.URL, string(html))
		if err != nil {
			crawlErrors[bd.doc.URL] = err
		} else {
			crawlResults[bd.doc.URL] = result
			w.fs.DeletePrefetchHTML(bd.doc.ID)
		}
	}(bd)
}
```

- [ ] **Step 3: Verify build**

```bash
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/worker/worker.go
git commit -m "feat: use prefetch HTML in worker doCrawl and processBatch"
```

---

### Task 4: Update HTTP handler to accept and store HTML

**Files:**
- Modify: `internal/server/http.go`
- Modify: `cmd/mykb-api/main.go`

- [ ] **Step 1: Add HTML field to ingestRequest and fsForHTTP interface**

In `internal/server/http.go`:

Add `HTML` field to `ingestRequest`:
```go
type ingestRequest struct {
	URL   string `json:"url"`
	HTML  string `json:"html"`
	Force bool   `json:"force"`
}
```

Add filesystem interface:
```go
// fsForHTTP is the subset of FilesystemStore used by the HTTP handler.
type fsForHTTP interface {
	WritePrefetchHTML(id string, html []byte) error
}
```

Update `NewHTTPHandler` signature to accept filesystem store:
```go
func NewHTTPHandler(pg pgForHTTP, w workerForHTTP, fs fsForHTTP) http.Handler {
```

Update `handleIngest` to accept and use `fs`:
```go
func handleIngest(pg pgForHTTP, w workerForHTTP, fs fsForHTTP) http.HandlerFunc {
```

In the handler function body, after `pg.InsertDocument` succeeds and before `w.Notify`, add:
```go
if req.HTML != "" {
	if err := fs.WritePrefetchHTML(doc.ID, []byte(req.HTML)); err != nil {
		log.Printf("server: failed to write prefetch HTML for %s: %v", doc.ID, err)
	}
}
```

Also add a request body size limit at the top of the handler:
```go
r.Body = http.MaxBytesReader(rw, r.Body, 32<<20) // 32 MB limit
```

- [ ] **Step 2: Update main.go to pass fs to NewHTTPHandler**

In `cmd/mykb-api/main.go`, change:
```go
httpHandler := server.NewHTTPHandler(pg, w)
```
to:
```go
httpHandler := server.NewHTTPHandler(pg, w, fs)
```

- [ ] **Step 3: Verify build**

```bash
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/server/http.go cmd/mykb-api/main.go
git commit -m "feat: accept HTML in /api/ingest and write prefetch file"
```

---

### Task 5: Update Firefox extension to capture and send HTML

**Files:**
- Modify: `extension/background.js`

- [ ] **Step 1: Update browser action click handler**

Replace the `browserAction.onClicked` listener in `extension/background.js`. The key change: before sending the POST, inject a content script to capture `outerHTML`:

```javascript
browser.browserAction.onClicked.addListener(async (tab) => {
  if (!tab.url || tab.url.startsWith("about:") || tab.url.startsWith("moz-extension:")) {
    setBadge("!", "#ff0000");
    setTimeout(clearBadge, 3000);
    return;
  }

  const server = await getServer();
  setBadge("...", "#ddaa00");

  try {
    // Capture rendered HTML from the active tab.
    const results = await browser.tabs.executeScript(tab.id, {
      code: "document.documentElement.outerHTML",
    });
    const html = results && results[0] ? results[0] : "";

    const resp = await fetch(`${server}/api/ingest`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url: tab.url, html }),
    });

    if (resp.status === 409) {
      setBadge("dup", "#4488ff");
      setTimeout(clearBadge, 3000);
      return;
    }

    if (!resp.ok) {
      setBadge("!", "#ff0000");
      setTimeout(clearBadge, 5000);
      return;
    }

    const data = await resp.json();
    pollStatus(server, data.id);
  } catch (e) {
    setBadge("ERR", "#ff0000");
    setTimeout(clearBadge, 5000);
  }
});
```

The only changes from the current version:
1. Added `browser.tabs.executeScript` call to capture `outerHTML`
2. Changed `body: JSON.stringify({ url: tab.url })` to `body: JSON.stringify({ url: tab.url, html })`

- [ ] **Step 2: Commit**

```bash
git add extension/background.js
git commit -m "feat: capture and send page HTML from Firefox extension"
```

---

### Task 6: Deploy and test

- [ ] **Step 1: Push to trigger image build**

```bash
git push origin main
```

Wait for GitHub Actions to complete.

- [ ] **Step 2: Restart mykb-api**

```bash
ssh hass.dayton 'kubectl rollout restart deployment/mykb-api -n mykb'
```

- [ ] **Step 3: Test via curl (simulate extension sending HTML)**

```bash
curl -s -X POST http://api.mykb.k3s/api/ingest \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://test-html-ingest.example.com","html":"<html><head><title>Test Page</title></head><body><h1>Hello</h1><p>This is a test page sent as raw HTML.</p></body></html>"}'
```

Expected: `{"id":"..."}` with status 201.

- [ ] **Step 4: Check logs for prefetch HTML usage**

```bash
ssh hass.dayton 'kubectl logs deployment/mykb-api -n mykb --tail=10'
```

Expected: `worker: using prefetch HTML for https://test-html-ingest.example.com (N bytes)`

- [ ] **Step 5: Reload Firefox extension and test**

1. Open `about:debugging#/runtime/this-firefox`
2. Click "Load Temporary Add-on" and select `extension/manifest.json`
3. Navigate to any page and click the extension icon
4. Verify badge shows progress (CRL → CHK → EMB → IDX → ✓)
5. Check server logs for "using prefetch HTML" message
