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
