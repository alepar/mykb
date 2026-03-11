package server

import (
	mykbv1 "mykb/gen/mykb/v1"
	"mykb/internal/config"
	"mykb/internal/search"
	"mykb/internal/storage"
	"mykb/internal/worker"
)

// Server implements the KBServiceServer gRPC interface.
type Server struct {
	mykbv1.UnimplementedKBServiceServer

	pg       *storage.PostgresStore
	fs       *storage.FilesystemStore
	qdrant   *storage.QdrantStore
	meili    *storage.MeilisearchStore
	searcher *search.HybridSearcher
	worker   *worker.Worker
	cfg      *config.Config
}

// NewServer creates a Server with all required dependencies.
func NewServer(
	pg *storage.PostgresStore,
	fs *storage.FilesystemStore,
	qdrant *storage.QdrantStore,
	meili *storage.MeilisearchStore,
	searcher *search.HybridSearcher,
	w *worker.Worker,
	cfg *config.Config,
) *Server {
	return &Server{
		pg:       pg,
		fs:       fs,
		qdrant:   qdrant,
		meili:    meili,
		searcher: searcher,
		worker:   w,
		cfg:      cfg,
	}
}
