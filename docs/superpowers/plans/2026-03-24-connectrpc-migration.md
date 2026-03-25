# ConnectRPC Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace google.golang.org/grpc with connectrpc.com/connect — single HTTP server, simplified k8s, JSON-over-HTTP for browser clients.

**Architecture:** Install protoc-gen-connect-go, regenerate proto, rewrite server interface to use connect types, rewrite CLI client, merge to single HTTP server on port 9091, simplify k8s to one service + one ingress.

**Tech Stack:** connectrpc.com/connect, protoc-gen-connect-go

**Spec:** `docs/superpowers/specs/2026-03-24-connectrpc-migration-design.md`

---

### Task 1: Install protoc-gen-connect-go, regenerate proto, update Justfile

**Files:**
- Modify: `Justfile`
- Delete: `gen/mykb/v1/kb_grpc.pb.go`
- Create: `gen/mykb/v1/mykbv1connect/` (generated)

- [ ] **Step 1: Install protoc-gen-connect-go**

```bash
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
```

Verify it's in PATH: `which protoc-gen-connect-go`

- [ ] **Step 2: Add connect dependency to go.mod**

```bash
go get connectrpc.com/connect@latest
```

- [ ] **Step 3: Update Justfile proto target**

Replace the `proto` target in `Justfile` (line 26):

Old:
```
proto:
    protoc --proto_path=proto --go_out=paths=source_relative:gen --go-grpc_out=paths=source_relative:gen mykb/v1/kb.proto
```

New:
```
proto:
    protoc --proto_path=proto --go_out=paths=source_relative:gen --connect-go_out=paths=source_relative:gen mykb/v1/kb.proto
```

- [ ] **Step 4: Delete old gRPC generated code**

```bash
rm gen/mykb/v1/kb_grpc.pb.go
```

- [ ] **Step 5: Regenerate proto**

```bash
just proto
```

Expected: `gen/mykb/v1/mykbv1connect/` directory created with `kb.connect.go`.

- [ ] **Step 6: Commit**

```bash
git add Justfile gen/
git commit -m "feat: switch proto codegen from grpc-go to connect-go"
```

Note: Build will fail after this until server.go is updated. This is expected.

---

### Task 2: Migrate server to Connect types

**Files:**
- Modify: `internal/server/server.go`

- [ ] **Step 1: Rewrite server.go**

Read the current `internal/server/server.go` first. Then rewrite it with these changes:

**Imports:** Replace gRPC imports with Connect:
```go
import (
	"context"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"

	mykbv1 "mykb/gen/mykb/v1"
	"mykb/internal/config"
	"mykb/internal/search"
	"mykb/internal/storage"
	"mykb/internal/worker"
)
```

**Server struct:** Remove `mykbv1.UnimplementedKBServiceServer` embed. The struct fields stay the same.

**Extract deleteDocument private method** from the current `DeleteDocument` RPC handler. The private method takes `(ctx, id string) error` and contains the actual delete logic (verify exists, delete from qdrant, meilisearch, filesystem, postgres). Both the public `DeleteDocument` RPC handler and the force-delete paths in `IngestURL`/`IngestURLs` call this private method.

```go
// deleteDocument removes a document from all stores.
func (s *Server) deleteDocument(ctx context.Context, id string) error {
	if _, err := s.pg.GetDocument(ctx, id); err != nil {
		return fmt.Errorf("document not found: %w", err)
	}
	if err := s.qdrant.DeleteByDocumentID(ctx, id); err != nil {
		return fmt.Errorf("delete from qdrant: %w", err)
	}
	if err := s.meili.DeleteByDocumentID(ctx, id); err != nil {
		return fmt.Errorf("delete from meilisearch: %w", err)
	}
	if err := s.fs.DeleteDocumentFiles(id); err != nil {
		return fmt.Errorf("delete files: %w", err)
	}
	if err := s.pg.DeleteDocument(ctx, id); err != nil {
		return fmt.Errorf("delete from postgres: %w", err)
	}
	return nil
}
```

**Method signature changes** — apply these patterns to each method:

For **unary RPCs** (Query, ListDocuments, GetDocuments, DeleteDocument):
- Old: `func (s *Server) Query(ctx context.Context, req *mykbv1.QueryRequest) (*mykbv1.QueryResponse, error)`
- New: `func (s *Server) Query(ctx context.Context, req *connect.Request[mykbv1.QueryRequest]) (*connect.Response[mykbv1.QueryResponse], error)`
- Access request fields via `req.Msg` instead of `req` directly (e.g., `req.Msg.GetQuery()`)
- Return `connect.NewResponse(msg), nil` instead of `msg, nil`
- Errors: `connect.NewError(connect.CodeInternal, fmt.Errorf(...))` instead of `status.Errorf(codes.Internal, ...)`

For **server-streaming RPCs** (IngestURL, IngestURLs):
- Old: `func (s *Server) IngestURL(req *mykbv1.IngestURLRequest, stream grpc.ServerStreamingServer[mykbv1.IngestProgress]) error`
- New: `func (s *Server) IngestURL(ctx context.Context, req *connect.Request[mykbv1.IngestURLRequest], stream *connect.ServerStream[mykbv1.IngestProgress]) error`
- Context is now a parameter (was `stream.Context()`)
- Access request via `req.Msg`
- `stream.Send(msg)` stays the same method name

**Force-delete in IngestURL and IngestURLs:** Replace `s.DeleteDocument(ctx, &mykbv1.DeleteDocumentRequest{Id: existing.ID})` with `s.deleteDocument(ctx, existing.ID)`.

- [ ] **Step 2: Verify build**

```bash
go build ./internal/server/
```

Expected: May fail if main.go still uses old gRPC types. That's OK — we fix main.go next.

- [ ] **Step 3: Commit**

```bash
git add internal/server/server.go
git commit -m "feat: migrate server to ConnectRPC types"
```

---

### Task 3: Rewrite main.go to single HTTP server

**Files:**
- Modify: `cmd/mykb-api/main.go`

- [ ] **Step 1: Rewrite main.go**

Replace the entire file. The key changes:
- Remove gRPC imports, add connect import
- Remove `net` import (no more TCP listener)
- Single `http.ServeMux` mounting both Connect handler and HTTP API handler
- Single `http.Server` on HTTPPort
- Add `/healthz` endpoint
- Remove gRPC server setup

```go
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"mykb/gen/mykb/v1/mykbv1connect"
	"mykb/internal/config"
	"mykb/internal/pipeline"
	"mykb/internal/ratelimit"
	"mykb/internal/search"
	"mykb/internal/server"
	"mykb/internal/storage"
	"mykb/internal/worker"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := config.Load()

	// Connect storage backends
	pg, err := storage.NewPostgresStore(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pg.Close()

	if err := pg.RunMigrations(ctx); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	fs := storage.NewFilesystemStore(cfg.DataDir)

	qdrant, err := storage.NewQdrantStore(cfg.QdrantGRPCHost, "mykb")
	if err != nil {
		log.Fatalf("qdrant: %v", err)
	}
	if err := qdrant.EnsureCollection(ctx, uint64(cfg.VoyageEmbedDimension)); err != nil {
		log.Fatalf("qdrant collection: %v", err)
	}

	meili, err := storage.NewMeilisearchStore(cfg.MeilisearchHost, cfg.MeilisearchKey, "mykb")
	if err != nil {
		log.Fatalf("meilisearch: %v", err)
	}
	if err := meili.EnsureIndex(ctx); err != nil {
		log.Fatalf("meilisearch index: %v", err)
	}

	// Pipeline components
	crawler := pipeline.NewCrawler(cfg.Crawl4AIURL)
	embedder := pipeline.NewEmbedder(cfg.VoyageAPIKey, cfg.VoyageEmbedModel, cfg.VoyageEmbedDimension)
	embedLimiter := ratelimit.NewAdaptiveLimiter(ratelimit.Config{
		StartingRate: 3.0,
	})
	defer embedLimiter.Close()
	embedder.SetLimiter(embedLimiter)
	indexer := pipeline.NewIndexer(qdrant, meili)

	// Search
	reranker := search.NewReranker(cfg.VoyageAPIKey, cfg.VoyageRerankModel)
	searcher := search.NewHybridSearcher(embedder, qdrant, meili, reranker, fs, cfg)

	// Worker
	w := worker.NewWorker(pg, fs, crawler, embedder, indexer, cfg)
	go w.Start(ctx)

	// HTTP server with Connect handler + REST API
	srv := server.NewServer(pg, fs, qdrant, meili, searcher, w, cfg)

	mux := http.NewServeMux()

	// Mount Connect RPC handler
	path, handler := mykbv1connect.NewKBServiceHandler(srv)
	mux.Handle(path, handler)

	// Mount REST API routes
	httpHandler := server.NewHTTPHandler(pg, w)
	mux.Handle("/api/", httpHandler)

	// Health check
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	httpServer := &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		_ = httpServer.Shutdown(context.Background())
		cancel()
	}()

	log.Printf("server listening on :%s", cfg.HTTPPort)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}
```

- [ ] **Step 2: Remove GRPCPort from config**

In `internal/config/config.go`: remove the `GRPCPort` field (line 15) and its initialization `GRPCPort: envOr("GRPC_PORT", "9090"),` (line 54).

- [ ] **Step 3: Verify build**

```bash
go build ./cmd/mykb-api/
```

Expected: Builds if server.go is correct. If it fails due to the generated connect interface not matching, fix server.go method signatures.

- [ ] **Step 4: Commit**

```bash
git add cmd/mykb-api/main.go internal/config/config.go
git commit -m "feat: replace gRPC server with single HTTP server using ConnectRPC"
```

---

### Task 4: Migrate CLI client

**Files:**
- Modify: `cmd/mykb/main.go`
- Modify: `internal/cliconfig/config.go`

- [ ] **Step 1: Update CLI config default**

In `internal/cliconfig/config.go`, change the default host on line 33 from `"localhost:9090"` to `"http://localhost:9091"`.

- [ ] **Step 2: Rewrite CLI imports and client creation**

In `cmd/mykb/main.go`:

Replace imports:
```go
// Remove:
"google.golang.org/grpc"
"google.golang.org/grpc/credentials/insecure"
mykbv1 "mykb/gen/mykb/v1"

// Add:
"net/http"
"connectrpc.com/connect"
mykbv1 "mykb/gen/mykb/v1"
"mykb/gen/mykb/v1/mykbv1connect"
```

Remove the `connect()` helper function entirely (lines 55-57).

Create clients directly in each command function using:
```go
client := mykbv1connect.NewKBServiceClient(http.DefaultClient, cfg.Host)
```

- [ ] **Step 3: Update runIngest**

Replace the gRPC streaming pattern with Connect streaming:

```go
func runIngest(args []string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "suppress progress, print ok/error only")
	force := fs.Bool("force", false, "re-ingest even if URL already exists")
	host := fs.String("host", "", "server address (default: from config)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: mykb ingest <url> [--quiet] [--force] [--host HOST]")
		os.Exit(1)
	}
	url := fs.Arg(0)

	cfg := cliconfig.Load("")
	if *host != "" {
		cfg.Host = *host
	}

	client := mykbv1connect.NewKBServiceClient(http.DefaultClient, cfg.Host)
	stream, err := client.IngestURL(context.Background(), connect.NewRequest(&mykbv1.IngestURLRequest{Url: url, Force: *force}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var lastStatus string
	var stepStart time.Time
	var progress strings.Builder

	for stream.Receive() {
		msg := stream.Msg()

		if *quiet {
			continue
		}

		status := msg.GetStatus()
		if status != lastStatus {
			if lastStatus != "" {
				elapsed := time.Since(stepStart)
				fmt.Fprintf(&progress, "(%.1fs)", elapsed.Seconds())
			}
			fmt.Fprintf(&progress, "..%s..", status)
			lastStatus = status
			stepStart = time.Now()
		}

		fmt.Fprintf(os.Stderr, "\r%s", progress.String())
	}
	if err := stream.Err(); err != nil {
		if *quiet {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		}
		os.Exit(1)
	}

	if *quiet {
		fmt.Println("ok")
	} else {
		if lastStatus != "" {
			elapsed := time.Since(stepStart)
			fmt.Fprintf(&progress, "(%.1fs)", elapsed.Seconds())
		}
		fmt.Fprintf(os.Stderr, "\r%s done.\n", progress.String())
	}
}
```

- [ ] **Step 4: Update runQuery**

Replace gRPC client with Connect client. Key changes:
- No `conn` / `defer conn.Close()`
- `client := mykbv1connect.NewKBServiceClient(http.DefaultClient, cfg.Host)`
- `resp, err := client.Query(ctx, connect.NewRequest(&mykbv1.QueryRequest{...}))` — returns `*connect.Response`
- Access results via `resp.Msg.Results` instead of `resp.Results`
- Same for `GetDocuments`: `docsResp, err := client.GetDocuments(ctx, connect.NewRequest(&mykbv1.GetDocumentsRequest{...}))` and `docsResp.Msg.Documents`

- [ ] **Step 5: Update runImportURLs**

Same pattern as runIngest:
- Create client via `mykbv1connect.NewKBServiceClient`
- Call `client.IngestURLs(ctx, connect.NewRequest(&mykbv1.IngestURLsRequest{...}))`
- Iterate via `for stream.Receive() { msg := stream.Msg(); ... }; if err := stream.Err(); ...`
- Remove `conn` / `defer conn.Close()`
- Remove `io.EOF` check (Connect streaming uses `Receive()/Err()` pattern)

- [ ] **Step 6: Remove unused imports**

After all changes, the `io` import may still be needed for other uses — check. Remove `google.golang.org/grpc` and `google.golang.org/grpc/credentials/insecure`.

- [ ] **Step 7: Verify build**

```bash
go build ./cmd/mykb/
```

- [ ] **Step 8: Commit**

```bash
git add cmd/mykb/main.go internal/cliconfig/config.go
git commit -m "feat: migrate CLI client from gRPC to ConnectRPC"
```

---

### Task 5: Update Dockerfile, docker-compose, k8s manifests

**Files:**
- Modify: `Dockerfile`
- Modify: `docker-compose.yml`
- Modify: `k8s/mykb-api-deployment.yaml`
- Rewrite: `k8s/mykb-api-service.yaml`
- Delete: `k8s/ingress-grpc.yaml`, `k8s/ingress-http.yaml`
- Create: `k8s/ingress.yaml`

- [ ] **Step 1: Update Dockerfile**

Remove `EXPOSE 9090` line. Keep `EXPOSE 9091`.

- [ ] **Step 2: Update docker-compose.yml**

Remove the `"9090:9090"` port mapping from the `mykb` service. Keep `"9091:9091"`.

- [ ] **Step 3: Update mykb-api-deployment.yaml**

Remove port 9090 (`grpc`), keep port 9091 (`http`). Change probes from TCP to HTTP:

```yaml
          ports:
            - containerPort: 9091
              name: http
          # ... (env stays the same)
          readinessProbe:
            httpGet:
              path: /healthz
              port: 9091
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /healthz
              port: 9091
            initialDelaySeconds: 5
            periodSeconds: 20
```

- [ ] **Step 4: Replace mykb-api-service.yaml**

Replace entire contents with a single service:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: mykb-api
  namespace: mykb
spec:
  selector:
    app: mykb-api
  ports:
    - port: 9091
      targetPort: 9091
  type: ClusterIP
```

- [ ] **Step 5: Delete old ingress files, create new ingress**

Delete `k8s/ingress-grpc.yaml` and `k8s/ingress-http.yaml`.

Create `k8s/ingress.yaml`:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: mykb-ingress
  namespace: mykb
spec:
  rules:
    - host: mykb.k3s
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: mykb-api
                port:
                  number: 9091
```

- [ ] **Step 6: Commit**

```bash
git add Dockerfile docker-compose.yml k8s/
git rm k8s/ingress-grpc.yaml k8s/ingress-http.yaml
git commit -m "feat: simplify deployment to single HTTP port (ConnectRPC)"
```

---

### Task 6: Clean up dependencies and update CLAUDE.md

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Remove unused gRPC dependencies**

```bash
go mod tidy
```

This should remove `google.golang.org/grpc` and related packages from go.mod (if no other code imports them — note that `internal/storage/qdrant.go` may still use gRPC for the Qdrant client, so `google.golang.org/grpc` might stay).

- [ ] **Step 2: Update CLAUDE.md**

Key changes:
- Update `just proto` command to show `--connect-go_out` instead of `--go-grpc_out`
- Update proto prerequisites: `protoc-gen-connect-go` instead of `protoc-gen-go-grpc`
- Update Docker Compose services table: remove port 9090, show only 9091 for mykb
- Update any references to "gRPC server" → "HTTP server (ConnectRPC)"
- Note that the CLI `--host` flag now takes an HTTP URL (e.g., `http://mykb.k3s`)

- [ ] **Step 3: Verify full build**

```bash
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum CLAUDE.md
git commit -m "chore: clean up dependencies and update CLAUDE.md for ConnectRPC"
```

---

### Task 7: Deploy and test

- [ ] **Step 1: Push to trigger image build**

```bash
git push origin main
```

Wait for GitHub Actions to complete.

- [ ] **Step 2: Re-deploy k8s manifests**

```bash
scp k8s/*.yaml hass.dayton:/tmp/mykb-k8s/
ssh hass.dayton 'kubectl delete ingress mykb-ingress-grpc mykb-ingress-http -n mykb 2>/dev/null; kubectl delete service mykb-api-grpc mykb-api-http -n mykb 2>/dev/null; kubectl apply -f /tmp/mykb-k8s/'
```

- [ ] **Step 3: Restart mykb-api**

```bash
ssh hass.dayton 'kubectl rollout restart deployment/mykb-api -n mykb'
```

Wait for pod to be ready.

- [ ] **Step 4: Build CLI and test query**

```bash
go build -o mykb ./cmd/mykb/
./mykb query --host http://mykb.k3s "test query"
```

Expected: Results returned (or "no results" if query doesn't match, but no connection error).

- [ ] **Step 5: Test JSON-over-HTTP (curl)**

Connect serves JSON natively. Test with curl:

```bash
curl -X POST http://mykb.k3s/mykb.v1.KBService/ListDocuments \
  -H 'Content-Type: application/json' \
  -d '{"limit": 3}'
```

Expected: JSON response with documents.

- [ ] **Step 6: Test ingest via CLI**

```bash
./mykb ingest --host http://mykb.k3s "https://example.com" --quiet
```

Expected: "ok" (or already-exists error if URL was previously ingested).

- [ ] **Step 7: Verify health endpoint**

```bash
curl http://mykb.k3s/healthz
```

Expected: "ok"
