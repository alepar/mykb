package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	mykbv1 "mykb/gen/mykb/v1"
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

	// gRPC server
	srv := server.NewServer(pg, fs, qdrant, meili, searcher, w, cfg)
	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	mykbv1.RegisterKBServiceServer(grpcServer, srv)
	reflection.Register(grpcServer)

	httpHandler := server.NewHTTPHandler(pg, w)
	httpServer := &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: httpHandler,
	}
	go func() {
		log.Printf("HTTP API server listening on :%s", cfg.HTTPPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		grpcServer.GracefulStop()
		_ = httpServer.Shutdown(context.Background())
		cancel()
	}()

	log.Printf("gRPC server listening on :%s", cfg.GRPCPort)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
