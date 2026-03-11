package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	mykbv1 "mykb/gen/mykb/v1"
	"mykb/internal/config"
	"mykb/internal/pipeline"
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

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		grpcServer.GracefulStop()
		cancel()
	}()

	log.Printf("gRPC server listening on :%s", cfg.GRPCPort)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
