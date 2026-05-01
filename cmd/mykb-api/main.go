package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mykb/gen/mykb/v1/mykbv1connect"
	"mykb/internal/config"
	"mykb/internal/pipeline"
	"mykb/internal/ratelimit"
	"mykb/internal/search"
	"mykb/internal/server"
	"mykb/internal/storage"
	"mykb/internal/worker"
)

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Connect-Protocol-Version")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const selfTestCanary = "mykb-selftest-canary-7x9k2"
const selfTestURL = "mykb://self-test"
const selfTestHTML = `<html><head><title>MyKB Self Test</title></head><body>
<h1>MyKB Self Test Document</h1>
<p>This document contains the unique canary phrase: ` + selfTestCanary + `</p>
<p>If you can read this in query results, the full ingestion pipeline is working correctly.</p>
</body></html>`

const wikiSelfTestURL = "wiki://healthz/__deep_test__.md"
const wikiSelfTestBody = "---\ntype: concept\ndate_updated: 2026-04-30\n---\n# Healthz\n\nDeep health check wiki ingest.\n"

func handleDeepHealth(
	pg *storage.PostgresStore,
	fs *storage.FilesystemStore,
	qdrant *storage.QdrantStore,
	meili *storage.MeilisearchStore,
	w *worker.Worker,
	searcher *search.HybridSearcher,
	wikiIngestor *pipeline.WikiIngestor,
) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()

		totalStart := time.Now()
		stages := map[string]string{}
		respond := func(status string, err string) {
			stages["total"] = time.Since(totalStart).Round(time.Millisecond).String()
			code := http.StatusOK
			if status == "fail" {
				code = http.StatusInternalServerError
			}
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(code)
			json.NewEncoder(rw).Encode(map[string]any{
				"status": status,
				"error":  err,
				"stages": stages,
			})
		}

		// Clean up any leftover test document.
		existing, _ := pg.GetDocumentByURL(ctx, selfTestURL)
		if existing.ID != "" {
			cleanupTestDoc(ctx, pg, qdrant, meili, fs, existing.ID)
		}

		// Ensure cleanup on exit.
		var docID string
		defer func() {
			if docID != "" {
				cleanupTestDoc(ctx, pg, qdrant, meili, fs, docID)
				stages["cleanup"] = "ok"
			}
		}()

		// 1. Ingest
		ingestStart := time.Now()
		doc, err := pg.InsertDocument(ctx, selfTestURL)
		if err != nil {
			respond("fail", fmt.Sprintf("insert: %v", err))
			return
		}
		docID = doc.ID

		// Write prefetch HTML so we don't need crawl4ai.
		if err := fs.WritePrefetchHTML(docID, []byte(selfTestHTML)); err != nil {
			respond("fail", fmt.Sprintf("write prefetch: %v", err))
			return
		}

		// Notify worker and wait for completion.
		progressCh := make(chan worker.ProgressUpdate, 32)
		w.NotifyWithProgress(docID, progressCh)

		for update := range progressCh {
			if update.Status == "DONE" {
				break
			}
			if update.Status == "ERROR" {
				stages["ingest"] = time.Since(ingestStart).Round(time.Millisecond).String()
				respond("fail", fmt.Sprintf("ingest error: %s", update.Message))
				return
			}
		}
		stages["ingest"] = time.Since(ingestStart).Round(time.Millisecond).String()

		// 2. Query
		queryStart := time.Now()
		results, err := searcher.Search(ctx, search.SearchParams{
			Query: selfTestCanary,
			TopK:  5,
		})
		if err != nil {
			stages["query"] = time.Since(queryStart).Round(time.Millisecond).String()
			respond("fail", fmt.Sprintf("query: %v", err))
			return
		}

		found := false
		for _, result := range results {
			if result.DocumentID == docID {
				found = true
				break
			}
		}
		stages["query"] = time.Since(queryStart).Round(time.Millisecond).String()

		if !found {
			respond("fail", "query: canary phrase not found in results")
			return
		}

		// 3. Wiki ingest smoke pass.
		wikiStart := time.Now()

		// Clean up any leftover wiki test document from a previous run.
		wikiExisting, _ := pg.GetDocumentByURL(ctx, wikiSelfTestURL)
		if wikiExisting.ID != "" {
			cleanupTestDoc(ctx, pg, qdrant, meili, fs, wikiExisting.ID)
		}

		// Defer cleanup of wiki test document regardless of outcome.
		var wikiDocID string
		defer func() {
			if wikiDocID != "" {
				cleanupTestDoc(context.Background(), pg, qdrant, meili, fs, wikiDocID)
			}
		}()

		wikiHash := pipeline.ComputeContentHash(wikiSelfTestBody)
		wikiResult, err := wikiIngestor.Ingest(ctx, wikiSelfTestURL, "Healthz", wikiSelfTestBody, wikiHash, false)
		stages["wiki_ingest"] = time.Since(wikiStart).Round(time.Millisecond).String()
		if err != nil {
			respond("fail", fmt.Sprintf("wiki ingest: %v", err))
			return
		}
		wikiDocID = wikiResult.DocumentID

		if wikiResult.Chunks < 1 {
			respond("fail", "wiki ingest: no chunks produced")
			return
		}

		respond("pass", "")
	}
}

func cleanupTestDoc(ctx context.Context, pg *storage.PostgresStore, qdrant *storage.QdrantStore, meili *storage.MeilisearchStore, fs *storage.FilesystemStore, id string) {
	_ = qdrant.DeleteByDocumentID(ctx, id)
	_ = meili.DeleteByDocumentID(ctx, id)
	_ = fs.DeleteDocumentFiles(id)
	_ = pg.DeleteDocument(ctx, id)
}

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
	hostname, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())
	log.Printf("worker ID: %s", workerID)

	w := worker.NewWorker(pg, fs, crawler, embedder, indexer, cfg, workerID)
	go w.Start(ctx)

	// Wiki ingestor for synchronous wiki document ingestion
	wikiIngestor := pipeline.NewWikiIngestor(pg, embedder, indexer, fs)

	// HTTP server with Connect handler + REST API
	srv := server.NewServer(pg, fs, qdrant, meili, searcher, w, cfg, wikiIngestor)

	mux := http.NewServeMux()

	// Mount Connect RPC handler
	path, handler := mykbv1connect.NewKBServiceHandler(srv)
	mux.Handle(path, handler)

	// Mount REST API routes
	httpHandler := server.NewHTTPHandler(pg, w, fs)
	mux.Handle("/api/", httpHandler)

	// Health check
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Deep health check — full pipeline smoke test
	mux.HandleFunc("GET /healthz/deep", handleDeepHealth(pg, fs, qdrant, meili, w, searcher, wikiIngestor))

	httpServer := &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: corsMiddleware(mux),
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
