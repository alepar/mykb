# Batch Crawl Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-document worker goroutines with a batch coordinator that sends multiple URLs in a single crawl4ai request, enabling true parallel crawling.

**Architecture:** A single coordinator goroutine pulls batches of N documents from the notify channel, sends non-Reddit URLs in one `/crawl` POST to crawl4ai (whose internal `arun_many` parallelizes across browser pages), then fans out embed+index into per-document goroutines gated by the existing rate limiter.

**Tech Stack:** Go, Crawl4AI `/crawl` endpoint (multi-URL)

**Spec:** `docs/superpowers/specs/2026-03-24-batch-crawl-design.md`

---

### Task 1: Add CrawlBatch method to Crawler

**Files:**
- Modify: `internal/pipeline/crawl.go`

- [ ] **Step 1: Add CrawlBatch method**

Add this method after the existing `Crawl` method. It reuses the existing `crawlRequest`, `crawlResponse`, and `crawlResult` types. The key difference from `crawlOnce` is: it sends multiple URLs and uses a longer timeout.

```go
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
		// Marshal error — all URLs fail.
		errs := make(map[string]error, len(urls))
		for _, u := range urls {
			errs[u] = err
		}
		return nil, errs
	}

	// Retry on transport-level failures only.
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

		// Parse per-URL results.
		return c.parseBatchResults(urls, &cr)
	}

	// All retries exhausted — every URL gets the transport error.
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

	// Build a map of URL -> crawlResult from the response.
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
```

Also add an `IsRedditURL` exported function so the worker can filter Reddit URLs before calling `CrawlBatch`:

```go
// IsRedditURL returns true if the URL should be crawled via the Reddit path.
func IsRedditURL(url string) bool {
	return isRedditThread(url)
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/crawl.go
git commit -m "feat: add CrawlBatch method for multi-URL crawl requests"
```

---

### Task 2: Add saveCrawlResult helper and batch coordinator to worker

**Files:**
- Modify: `internal/worker/worker.go`

- [ ] **Step 1: Add saveCrawlResult helper**

Extract the post-crawl logic from `doCrawl` (lines 210-234) into a new method. This handles saving markdown, setting title, setting `crawled_at`, and sending progress — everything `doCrawl` does after the `Crawl()` call:

```go
// saveCrawlResult persists a crawl result (markdown, title, crawled_at) and sends progress.
// Used by the batch coordinator after CrawlBatch returns.
func (w *Worker) saveCrawlResult(ctx context.Context, doc *storage.Document, result pipeline.CrawlResult, progress chan<- ProgressUpdate) error {
	if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "CRAWLING"); err != nil {
		return fmt.Errorf("set status CRAWLING: %w", err)
	}

	if err := w.fs.WriteDocument(doc.ID, []byte(result.Markdown)); err != nil {
		return fmt.Errorf("write document: %w", err)
	}

	if result.RawMarkdown != "" {
		if err := w.fs.WriteDocumentRaw(doc.ID, []byte(result.RawMarkdown)); err != nil {
			return fmt.Errorf("write raw document: %w", err)
		}
	}

	if result.Title != "" {
		if err := w.pg.SetDocumentTitle(ctx, doc.ID, result.Title); err != nil {
			return fmt.Errorf("set title: %w", err)
		}
	}

	if err := w.pg.SetDocumentCrawledAt(ctx, doc.ID); err != nil {
		return fmt.Errorf("set crawled_at: %w", err)
	}

	sendProgress(progress, ProgressUpdate{
		DocumentID: doc.ID,
		Status:     "CRAWLING",
		Message:    "Crawl complete",
	})

	return nil
}
```

- [ ] **Step 2: Replace worker pool with batch coordinator**

Replace the entire worker pool section in `Start()` (from `// Launch worker pool.` through `wg.Wait()`) with the batch coordinator:

```go
	// Launch batch coordinator.
	batchSize := w.cfg.WorkerConcurrency
	if batchSize < 1 {
		batchSize = 1
	}
	log.Printf("worker: starting batch coordinator (batch size %d)", batchSize)

	for {
		// Pull a batch of items from the channel.
		batch := w.pullBatch(ctx, batchSize)
		if len(batch) == 0 {
			return // context cancelled
		}

		w.processBatch(ctx, batch)
	}
```

- [ ] **Step 3: Add pullBatch method**

```go
// pullBatch drains up to maxSize items from the notify channel.
// Blocks until at least one item is available, then collects more with a short timeout.
func (w *Worker) pullBatch(ctx context.Context, maxSize int) []workItem {
	var batch []workItem

	// Block until first item or context cancellation.
	select {
	case <-ctx.Done():
		return nil
	case item := <-w.notify:
		batch = append(batch, item)
	}

	// Collect more items with a short timeout.
	timeout := time.After(100 * time.Millisecond)
	for len(batch) < maxSize {
		select {
		case item := <-w.notify:
			batch = append(batch, item)
		case <-timeout:
			return batch
		case <-ctx.Done():
			return batch
		}
	}

	return batch
}
```

Add `"time"` to imports.

- [ ] **Step 4: Add processBatch method**

```go
// processBatch crawls a batch of URLs together, then chunks and fans out embed+index.
func (w *Worker) processBatch(ctx context.Context, batch []workItem) {
	// Load documents from postgres.
	type batchDoc struct {
		item workItem
		doc  storage.Document
	}
	var docs []batchDoc
	for _, item := range batch {
		doc, err := w.pg.GetDocument(ctx, item.documentID)
		if err != nil {
			log.Printf("worker: failed to get document %s: %v", item.documentID, err)
			if item.progress != nil {
				sendProgress(item.progress, ProgressUpdate{
					DocumentID: item.documentID, Status: "ERROR", Message: err.Error(),
				})
				close(item.progress)
			}
			continue
		}
		// Clear error if retrying.
		if doc.Error != nil {
			if err := w.pg.ClearDocumentError(ctx, doc.ID); err != nil {
				log.Printf("worker: failed to clear error on document %s: %v", doc.ID, err)
			}
		}
		docs = append(docs, batchDoc{item: item, doc: doc})
	}

	if len(docs) == 0 {
		return
	}

	// Separate Reddit URLs from regular URLs.
	var regularDocs []batchDoc
	var redditDocs []batchDoc
	for _, bd := range docs {
		if pipeline.IsRedditURL(bd.doc.URL) {
			redditDocs = append(redditDocs, bd)
		} else {
			regularDocs = append(regularDocs, bd)
		}
	}

	// Crawl regular URLs in batch, Reddit URLs individually — all concurrently.
	crawlResults := make(map[string]pipeline.CrawlResult)
	crawlErrors := make(map[string]error)
	var crawlWg sync.WaitGroup

	// Batch crawl for regular URLs.
	if len(regularDocs) > 0 {
		crawlWg.Add(1)
		go func() {
			defer crawlWg.Done()
			urls := make([]string, len(regularDocs))
			for i, bd := range regularDocs {
				urls[i] = bd.doc.URL
			}
			log.Printf("worker: batch crawling %d URLs", len(urls))
			results, errs := w.crawler.CrawlBatch(ctx, urls)
			for url, result := range results {
				crawlResults[url] = result
			}
			for url, err := range errs {
				crawlErrors[url] = err
			}
		}()
	}

	// Individual crawl for Reddit URLs.
	for _, bd := range redditDocs {
		crawlWg.Add(1)
		go func(bd batchDoc) {
			defer crawlWg.Done()
			result, err := w.crawler.Crawl(ctx, bd.doc.URL)
			if err != nil {
				crawlErrors[bd.doc.URL] = err
			} else {
				crawlResults[bd.doc.URL] = result
			}
		}(bd)
	}

	crawlWg.Wait()

	// Process crawl results: save + chunk for successful crawls, handleError for failures.
	var embedDocs []batchDoc
	for i := range docs {
		bd := &docs[i]
		url := bd.doc.URL

		if crawlErr, failed := crawlErrors[url]; failed {
			w.handleError(ctx, bd.doc.ID, fmt.Errorf("crawl: %w", crawlErr))
			if bd.item.progress != nil {
				sendProgress(bd.item.progress, ProgressUpdate{
					DocumentID: bd.doc.ID, Status: "ERROR", Message: crawlErr.Error(),
				})
				close(bd.item.progress)
			}
			log.Printf("worker: crawl failed for %s: %v", url, crawlErr)
			continue
		}

		result := crawlResults[url]
		if err := w.saveCrawlResult(ctx, &bd.doc, result, bd.item.progress); err != nil {
			w.handleError(ctx, bd.doc.ID, err)
			if bd.item.progress != nil {
				sendProgress(bd.item.progress, ProgressUpdate{
					DocumentID: bd.doc.ID, Status: "ERROR", Message: err.Error(),
				})
				close(bd.item.progress)
			}
			log.Printf("worker: save crawl result failed for %s: %v", url, err)
			continue
		}

		if err := w.doChunk(ctx, &bd.doc, bd.item.progress); err != nil {
			w.handleError(ctx, bd.doc.ID, err)
			if bd.item.progress != nil {
				sendProgress(bd.item.progress, ProgressUpdate{
					DocumentID: bd.doc.ID, Status: "ERROR", Message: err.Error(),
				})
				close(bd.item.progress)
			}
			log.Printf("worker: chunk failed for %s: %v", url, err)
			continue
		}

		embedDocs = append(embedDocs, *bd)
	}

	// Fan out embed + index into goroutines (rate limited per-doc).
	var embedWg sync.WaitGroup
	for _, bd := range embedDocs {
		embedWg.Add(1)
		go func(bd batchDoc) {
			defer embedWg.Done()
			vectors := make(map[string][]float32)

			var docErr error
			if err := w.doEmbed(ctx, &bd.doc, bd.item.progress, vectors); err != nil {
				docErr = err
			} else if err := w.doIndex(ctx, &bd.doc, bd.item.progress, vectors); err != nil {
				docErr = err
			}

			if bd.item.progress != nil {
				if docErr != nil {
					sendProgress(bd.item.progress, ProgressUpdate{
						DocumentID: bd.doc.ID, Status: "ERROR", Message: docErr.Error(),
					})
				}
				close(bd.item.progress)
			}
			if docErr != nil {
				w.handleError(ctx, bd.doc.ID, docErr)
				log.Printf("worker: embed/index failed for %s: %v", bd.doc.URL, docErr)
			}
		}(bd)
	}
	embedWg.Wait()
}
```

- [ ] **Step 5: Verify build**

Run: `go build ./...`

- [ ] **Step 6: Commit**

```bash
git add internal/worker/worker.go
git commit -m "feat: replace worker pool with batch coordinator for parallel crawling"
```

---

### Task 3: Deploy and test

- [ ] **Step 1: Push to trigger image build**

```bash
git push origin main
```

Wait for GitHub Actions to complete.

- [ ] **Step 2: Restart mykb-api**

```bash
ssh hass.dayton 'kubectl rollout restart deployment/mykb-api -n mykb'
```

- [ ] **Step 3: Check logs for batch crawling**

```bash
ssh hass.dayton 'kubectl logs -f deployment/mykb-api -n mykb --tail=20'
```

Expected: `worker: batch crawling N URLs` log lines showing multiple URLs per batch.

- [ ] **Step 4: Test with a small batch**

```bash
head -10 urls.txt > /tmp/test-batch.txt
./mykb import-urls --file /tmp/test-batch.txt --host mykb.k3s:80 --force
```

Expected: Progress updates flowing, multiple URLs crawled in parallel (visible in crawl4ai logs as overlapping FETCH/SCRAPE/COMPLETE for different URLs).

- [ ] **Step 5: Verify crawl4ai logs show parallelism**

```bash
ssh hass.dayton 'kubectl logs deployment/crawl4ai -n mykb --tail=30'
```

Expected: Multiple `[FETCH]` lines interleaved (not strictly sequential `active=1`).
