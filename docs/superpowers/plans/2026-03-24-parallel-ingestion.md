# Parallel Ingestion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add parallel document ingestion with 8 concurrent workers, adaptive Voyage AI rate limiting, Crawl4AI deployment tuning, and a batch `IngestURLs` gRPC RPC with `import-urls` CLI command.

**Architecture:** Worker pool of N goroutines sharing a notify channel, gated by an adaptive rate limiter on the Voyage AI embedding API. New `IngestURLs` RPC streams per-document progress via a `sync.Map`-based progress bus. Crawl4AI retries on transient errors; its concurrency is managed internally via `MAX_CONCURRENT_TASKS`.

**Tech Stack:** Go, gRPC/protobuf, Kubernetes YAML

**Spec:** `docs/superpowers/specs/2026-03-24-parallel-ingestion-design.md`

---

### Task 1: Port adaptive rate limiter

**Files:**
- Create: `internal/ratelimit/adaptive.go`
- Create: `internal/ratelimit/adaptive_test.go`

- [ ] **Step 1: Copy adaptive limiter from movies project**

Copy `~/AleCode/meilisearch-movies/backend/internal/ratelimit/adaptive.go` to `internal/ratelimit/adaptive.go`. Change the package declaration from `package ratelimit` — it stays the same since both projects use the same package name. No other changes needed — the code is self-contained.

- [ ] **Step 2: Copy tests**

Copy `~/AleCode/meilisearch-movies/backend/internal/ratelimit/adaptive_test.go` to `internal/ratelimit/adaptive_test.go`.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/ratelimit/ -v`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/ratelimit/
git commit -m "feat: port adaptive rate limiter from meilisearch-movies"
```

---

### Task 2: Add WORKER_CONCURRENCY config

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add WorkerConcurrency field to Config struct**

Add `WorkerConcurrency int` to the `Config` struct (after `MaxRetries`), and add `WorkerConcurrency: envOrInt("WORKER_CONCURRENCY", 8),` in `Load()`.

Add helper function:

```go
func envOrInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}
```

Add `"strconv"` to imports.

- [ ] **Step 2: Run build**

Run: `go build ./...`
Expected: Builds successfully.

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add WORKER_CONCURRENCY config (default 8)"
```

---

### Task 3: Add retry with exponential backoff to embed.go

**Files:**
- Modify: `internal/pipeline/embed.go`

- [ ] **Step 1: Add rate limiter field and retry logic to Embedder**

Add a `limiter` field to the `Embedder` struct and a `SetLimiter` method. Add `embedWithRetry` method that wraps `postContextualized` with retry + limiter integration:

```go
// At the top of embed.go, add to imports:
// "time"
// "mykb/internal/ratelimit"

// Add to Embedder struct:
// limiter *ratelimit.AdaptiveLimiter

// Add method:
func (e *Embedder) SetLimiter(l *ratelimit.AdaptiveLimiter) {
	e.limiter = l
}
```

Modify `EmbedChunks` to call `embedWithRetry` instead of directly calling `postContextualized`:

```go
func (e *Embedder) EmbedChunks(ctx context.Context, chunks []string) ([][]float32, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	resp, err := e.embedWithRetry(ctx, [][]string{chunks}, "document", len(chunks))
	if err != nil {
		return nil, fmt.Errorf("embed chunks: %w", err)
	}

	if len(resp.Data) == 0 || len(resp.Data[0].Data) == 0 {
		return nil, fmt.Errorf("embed chunks: empty response")
	}

	log.Printf("embed [%s]: chunks=%d tokens=%d dims=%d",
		e.model, len(chunks), resp.Usage.TotalTokens, len(resp.Data[0].Data[0].Embedding))

	embeddings := make([][]float32, len(resp.Data[0].Data))
	for _, item := range resp.Data[0].Data {
		embeddings[item.Index] = item.Embedding
	}
	return embeddings, nil
}

const (
	embedMaxRetries = 5
	embedBaseDelay  = 4 * time.Second
)

func (e *Embedder) embedWithRetry(ctx context.Context, inputs [][]string, inputType string, expectedCount int) (*ctxEmbedResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= embedMaxRetries; attempt++ {
		if attempt > 0 {
			delay := embedBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("embed: retry %d/%d after %v", attempt, embedMaxRetries, delay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		if e.limiter != nil {
			e.limiter.Acquire()
		}

		resp, err := e.postContextualized(ctx, inputs, inputType)
		if err != nil {
			lastErr = err
			if e.limiter != nil {
				e.limiter.ReportFailure()
			}
			continue
		}

		// Validate response
		if len(resp.Data) == 0 || len(resp.Data[0].Data) != expectedCount {
			got := 0
			if len(resp.Data) > 0 {
				got = len(resp.Data[0].Data)
			}
			lastErr = fmt.Errorf("embedding response size mismatch: got %d, expected %d",
				got, expectedCount)
			if e.limiter != nil {
				e.limiter.ReportFailure()
			}
			continue
		}

		if e.limiter != nil {
			e.limiter.ReportSuccess()
		}
		return resp, nil
	}
	return nil, fmt.Errorf("embed failed after %d retries: %w", embedMaxRetries, lastErr)
}
```

Note: `EmbedQuery` (used at search time) intentionally does not use retry or the rate limiter. Search-time queries are single strings, far less likely to trigger rate limits, and search latency matters more than batch throughput.

- [ ] **Step 2: Run build**

Run: `go build ./...`
Expected: Builds successfully.

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/embed.go
git commit -m "feat: add retry with exponential backoff and rate limiter to embedder"
```

---

### Task 4: Add retry with exponential backoff to crawl.go

**Files:**
- Modify: `internal/pipeline/crawl.go`

- [ ] **Step 1: Wrap Crawl method with retry logic**

Rename the entire existing `Crawl` method to `crawlOnce` (including the `isRedditThread` dispatch at the top — the whole method body moves). Create new `Crawl` that calls `crawlOnce` with retry:

```go
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
			continue
		}
		return result, nil
	}
	return CrawlResult{}, fmt.Errorf("crawl failed after %d retries: %w", crawlMaxRetries, lastErr)
}
```

Rename the existing `Crawl` method to `crawlOnce` (keeping the same body). Add `"log"` and `"time"` to imports if not already present.

- [ ] **Step 2: Run build**

Run: `go build ./...`
Expected: Builds successfully.

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/crawl.go
git commit -m "feat: add retry with exponential backoff to crawler"
```

---

### Task 5: Convert worker to pool with increased channel capacity

**Files:**
- Modify: `internal/worker/worker.go`

- [ ] **Step 1: Increase channel capacity and add pool to Start()**

In `NewWorker`, change channel capacity from 64 to 8192:

```go
notify: make(chan workItem, 8192),
```

Add a `NotifyBlocking` method for batch ingestion:

```go
// NotifyBlocking enqueues a document for processing with a progress channel.
// Blocks if the channel is full — used by batch ingestion to guarantee delivery.
func (w *Worker) NotifyBlocking(ctx context.Context, documentID string, progress chan<- ProgressUpdate) error {
	select {
	case w.notify <- workItem{documentID: documentID, progress: progress}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

Modify `Start()` to resume once, then launch N goroutines:

```go
func (w *Worker) Start(ctx context.Context) {
	// Resume interrupted docs (once, before spawning pool).
	docs, err := w.pg.GetPendingDocuments(ctx, w.cfg.MaxRetries)
	if err != nil {
		log.Printf("worker: failed to get pending documents: %v", err)
	}
	for _, doc := range docs {
		if ctx.Err() != nil {
			return
		}
		if err := w.ProcessDocument(ctx, doc.ID, nil); err != nil {
			log.Printf("worker: error processing document %s: %v", doc.ID, err)
		}
	}

	// Launch worker pool.
	concurrency := w.cfg.WorkerConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	log.Printf("worker: starting pool with %d workers", concurrency)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item := <-w.notify:
					err := w.ProcessDocument(ctx, item.documentID, item.progress)
					if item.progress != nil {
						// Send terminal error status if processing failed, so
						// IngestURLs handler can count errors correctly.
						if err != nil {
							sendProgress(item.progress, ProgressUpdate{
								DocumentID: item.documentID,
								Status:     "ERROR",
								Message:    err.Error(),
							})
						}
						close(item.progress)
					}
					if err != nil {
						log.Printf("worker: error processing document %s: %v", item.documentID, err)
					}
				}
			}
		}()
	}
	wg.Wait()
}
```

Add `"sync"` to imports.

- [ ] **Step 2: Run build**

Run: `go build ./...`
Expected: Builds successfully.

- [ ] **Step 3: Commit**

```bash
git add internal/worker/worker.go
git commit -m "feat: convert worker to pool of N concurrent goroutines"
```

---

### Task 6: Wire rate limiter and concurrency in main.go

**Files:**
- Modify: `cmd/mykb-api/main.go`

- [ ] **Step 1: Create limiter at startup and pass to embedder**

After creating the embedder, create the limiter and attach it:

```go
// After: embedder := pipeline.NewEmbedder(...)
// Add:
import "mykb/internal/ratelimit"

embedLimiter := ratelimit.NewAdaptiveLimiter(ratelimit.Config{
	StartingRate: 0.4,
})
defer embedLimiter.Close()
embedder.SetLimiter(embedLimiter)
```

- [ ] **Step 2: Run build**

Run: `go build ./...`
Expected: Builds successfully.

- [ ] **Step 3: Commit**

```bash
git add cmd/mykb-api/main.go
git commit -m "feat: wire adaptive rate limiter for Voyage AI embeddings"
```

---

### Task 7: Add IngestURLs proto and regenerate

**Files:**
- Modify: `proto/mykb/v1/kb.proto`
- Regenerate: `gen/mykb/v1/kb.pb.go`, `gen/mykb/v1/kb_grpc.pb.go`

- [ ] **Step 1: Add IngestURLs RPC and messages to proto**

Add to the `KBService` service block (after `IngestURL`):

```protobuf
rpc IngestURLs(IngestURLsRequest) returns (stream IngestURLsProgress);
```

Add messages after `IngestProgress`:

```protobuf
message IngestURLsRequest {
  repeated string urls = 1;
  bool force = 2;
}

message IngestURLsProgress {
  int32 current = 1;
  int32 total = 2;
  string url = 3;
  string stage = 4;
  string error = 5;
}
```

- [ ] **Step 2: Regenerate gRPC code**

Run: `just proto`
Expected: Files regenerated in `gen/mykb/v1/`.

- [ ] **Step 3: Run build**

Run: `go build ./...`
Expected: Build fails because `IngestURLs` is not implemented on the server yet. This is expected.

- [ ] **Step 4: Commit**

```bash
git add proto/mykb/v1/kb.proto gen/mykb/v1/
git commit -m "feat: add IngestURLs batch RPC to proto"
```

---

### Task 8: Implement IngestURLs server handler with progress bus

**Files:**
- Modify: `internal/server/server.go`

- [ ] **Step 1: Implement IngestURLs method**

Add the `IngestURLs` method to `Server`:

```go
func (s *Server) IngestURLs(req *mykbv1.IngestURLsRequest, stream grpc.ServerStreamingServer[mykbv1.IngestURLsProgress]) error {
	ctx := stream.Context()
	urls := req.GetUrls()
	total := int32(len(urls))

	if total == 0 {
		return status.Errorf(codes.InvalidArgument, "no URLs provided")
	}

	// Track documents in this batch for progress reporting.
	type docInfo struct {
		id  string
		url string
	}
	var batchDocs []docInfo
	var skipped int32
	var current int32

	// Create documents and set up progress channels.
	progressChans := make([]chan worker.ProgressUpdate, 0, len(urls))

	for _, url := range urls {
		// Handle force re-ingest.
		if req.GetForce() {
			existing, err := s.pg.GetDocumentByURL(ctx, url)
			if err == nil && existing.ID != "" {
				if _, err := s.DeleteDocument(ctx, &mykbv1.DeleteDocumentRequest{Id: existing.ID}); err != nil {
					log.Printf("server: force delete of existing document %s failed: %v", existing.ID, err)
				}
			}
		}

		doc, err := s.pg.InsertDocument(ctx, url)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
				// Skip duplicate, report as skipped.
				skipped++
				current++
				if err := stream.Send(&mykbv1.IngestURLsProgress{
					Current: current,
					Total:   total,
					Url:     url,
					Stage:   "skipped",
				}); err != nil {
					return err
				}
				continue
			}
			return status.Errorf(codes.Internal, "insert document for %s: %v", url, err)
		}

		ch := make(chan worker.ProgressUpdate, 32)
		progressChans = append(progressChans, ch)
		batchDocs = append(batchDocs, docInfo{id: doc.ID, url: url})
	}

	// Queue all documents into the worker (blocking to guarantee delivery).
	for i, doc := range batchDocs {
		if err := s.worker.NotifyBlocking(ctx, doc.id, progressChans[i]); err != nil {
			return status.Errorf(codes.Internal, "queue document %s: %v", doc.url, err)
		}
	}

	// Stream progress from all documents.
	for i, ch := range progressChans {
		doc := batchDocs[i]
		for update := range ch {
			stage := strings.ToLower(update.Status)
			if stage == "" {
				stage = "processing"
			}

			errMsg := ""
			if stage == "error" {
				errMsg = update.Message
				current++
			} else if stage == "done" {
				current++
			}

			if err := stream.Send(&mykbv1.IngestURLsProgress{
				Current: current,
				Total:   total,
				Url:     doc.url,
				Stage:   stage,
				Error:   errMsg,
			}); err != nil {
				return err
			}
		}
	}

	return nil
}
```

Note: This implementation reads progress channels sequentially per document (all events for doc 0, then doc 1, etc). This is simpler than a multiplexed approach. The `current` counter reflects documents completed so far, but the client will see updates for one document at a time rather than interleaved progress from multiple workers. This is a known simplification — a multiplexed approach using `reflect.Select` or a merge goroutine could provide interleaved progress, but the sequential approach is sufficient for the initial implementation. The throughput bottleneck is the Voyage API, not progress reporting.

- [ ] **Step 2: Run build**

Run: `go build ./...`
Expected: Builds successfully.

- [ ] **Step 3: Commit**

```bash
git add internal/server/server.go
git commit -m "feat: implement IngestURLs batch RPC with progress streaming"
```

---

### Task 9: Add import-urls CLI command

**Files:**
- Modify: `cmd/mykb/main.go`

- [ ] **Step 1: Add import-urls to the switch and printUsage**

Add case to the switch in `main()`:

```go
case "import-urls":
	runImportURLs(os.Args[2:])
```

Add to `printUsage()`:

```go
fmt.Fprintln(os.Stderr, "  mykb import-urls --file FILE [--force] [--quiet] [--host HOST]")
```

- [ ] **Step 2: Implement runImportURLs**

```go
func runImportURLs(args []string) {
	fs := flag.NewFlagSet("import-urls", flag.ExitOnError)
	file := fs.String("file", "", "path to URL file (one URL per line)")
	force := fs.Bool("force", false, "re-ingest even if URL already exists")
	quiet := fs.Bool("quiet", false, "suppress progress output")
	host := fs.String("host", "", "server address (default: from config)")
	// Short aliases
	fs.StringVar(file, "f", "", "path to URL file (short for --file)")
	fs.Parse(args)

	if *file == "" {
		fmt.Fprintln(os.Stderr, "Usage: mykb import-urls --file FILE [--force] [--quiet] [--host HOST]")
		os.Exit(1)
	}

	// Read URLs from file.
	urls, err := readURLFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", *file, err)
		os.Exit(1)
	}

	if len(urls) == 0 {
		fmt.Println("no URLs found in file")
		return
	}

	cfg := cliconfig.Load("")
	if *host != "" {
		cfg.Host = *host
	}

	conn, err := connect(cfg.Host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := mykbv1.NewKBServiceClient(conn)
	stream, err := client.IngestURLs(context.Background(), &mykbv1.IngestURLsRequest{
		Urls:  urls,
		Force: *force,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var done, errors, skipped int
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			os.Exit(1)
		}

		switch msg.GetStage() {
		case "done":
			done++
		case "error":
			errors++
		case "skipped":
			skipped++
		}

		if !*quiet {
			fmt.Fprintf(os.Stderr, "\r[%d/%d] %s %s | done: %d | skipped: %d | errors: %d",
				msg.GetCurrent(), msg.GetTotal(), msg.GetStage(), truncateURL(msg.GetUrl(), 60),
				done, skipped, errors)
		}
	}

	if !*quiet {
		fmt.Fprintln(os.Stderr)
	}
	fmt.Printf("done: %d, skipped: %d, errors: %d (total: %d)\n", done, skipped, errors, len(urls))

	if errors > 0 {
		os.Exit(1)
	}
}

func readURLFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var urls []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	return urls, scanner.Err()
}

func truncateURL(url string, maxLen int) string {
	if len(url) <= maxLen {
		return url
	}
	return url[:maxLen-3] + "..."
}
```

- [ ] **Step 3: Run build**

Run: `go build ./...`
Expected: Builds successfully.

- [ ] **Step 4: Build CLI**

Run: `just cli`
Expected: `mykb` binary built.

- [ ] **Step 5: Commit**

```bash
git add cmd/mykb/main.go
git commit -m "feat: add import-urls CLI command for batch ingestion"
```

---

### Task 10: Update Crawl4AI k8s deployment

**Files:**
- Modify: `k8s/crawl4ai-deployment.yaml`

- [ ] **Step 1: Bump resources, add /dev/shm, add MAX_CONCURRENT_TASKS**

Update `k8s/crawl4ai-deployment.yaml` to:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: crawl4ai
  namespace: mykb
  labels:
    app: crawl4ai
spec:
  replicas: 1
  selector:
    matchLabels:
      app: crawl4ai
  template:
    metadata:
      labels:
        app: crawl4ai
    spec:
      nodeSelector:
        kubernetes.io/hostname: hass
      containers:
        - name: crawl4ai
          image: unclecode/crawl4ai:latest
          ports:
            - containerPort: 11235
          env:
            - name: MAX_CONCURRENT_TASKS
              value: "5"
          resources:
            requests:
              memory: "1Gi"
              cpu: "250m"
            limits:
              memory: "4Gi"
              cpu: "1000m"
          volumeMounts:
            - name: dshm
              mountPath: /dev/shm
          readinessProbe:
            httpGet:
              path: /health
              port: 11235
            initialDelaySeconds: 10
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /health
              port: 11235
            initialDelaySeconds: 30
            periodSeconds: 20
      volumes:
        - name: dshm
          emptyDir:
            medium: Memory
            sizeLimit: 1Gi
```

- [ ] **Step 2: Commit**

```bash
git add k8s/crawl4ai-deployment.yaml
git commit -m "feat(k8s): tune crawl4ai for concurrent crawling (4Gi mem, /dev/shm, MAX_CONCURRENT_TASKS)"
```

---

### Task 11: Deploy, rebuild image, and test

This task involves deploying to k8s and running a real test.

- [ ] **Step 1: Push to GitHub to trigger image build**

```bash
git push origin main
```

Wait for the GitHub Actions build to complete.

- [ ] **Step 2: Re-deploy crawl4ai and restart mykb-api**

Copy updated manifests to hass and apply:

```bash
scp k8s/crawl4ai-deployment.yaml hass.dayton:/tmp/mykb-k8s/
ssh hass.dayton 'kubectl apply -f /tmp/mykb-k8s/crawl4ai-deployment.yaml'
ssh hass.dayton 'kubectl rollout restart deployment/mykb-api -n mykb'
```

- [ ] **Step 3: Verify pods are running**

```bash
ssh hass.dayton 'kubectl get pods -n mykb'
```

Expected: All 5 pods running. Crawl4ai may take a moment to restart with the larger memory allocation.

- [ ] **Step 4: Test batch ingestion with a small batch**

Create a test file with 3 URLs:

```bash
head -3 urls.txt > /tmp/test-urls.txt
./mykb import-urls --file /tmp/test-urls.txt --host mykb.k3s:80
```

Expected: Progress updates for each URL, summary at end showing done/skipped/errors.

- [ ] **Step 5: Verify ingested documents are queryable**

```bash
./mykb query --host mykb.k3s:80 "test query related to ingested content"
```

Expected: Results from the newly ingested documents.
