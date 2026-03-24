package server

import (
	"context"
	"log"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mykbv1 "mykb/gen/mykb/v1"
	"mykb/internal/config"
	"mykb/internal/search"
	"mykb/internal/storage"
	"mykb/internal/worker"
)

// Server implements the mykbv1.KBServiceServer interface.
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

// NewServer creates a Server wired to all dependencies.
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

// IngestURL inserts a document for the given URL and streams progress updates
// as the worker processes it through the ingestion pipeline.
func (s *Server) IngestURL(req *mykbv1.IngestURLRequest, stream grpc.ServerStreamingServer[mykbv1.IngestProgress]) error {
	ctx := stream.Context()

	// If force is set, delete the existing document first.
	if req.GetForce() {
		existing, err := s.pg.GetDocumentByURL(ctx, req.GetUrl())
		if err == nil && existing.ID != "" {
			if _, err := s.DeleteDocument(ctx, &mykbv1.DeleteDocumentRequest{Id: existing.ID}); err != nil {
				log.Printf("server: force delete of existing document %s failed: %v", existing.ID, err)
			}
		}
	}

	doc, err := s.pg.InsertDocument(ctx, req.GetUrl())
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			return status.Errorf(codes.AlreadyExists, "URL already ingested: %s", req.GetUrl())
		}
		return status.Errorf(codes.Internal, "insert document: %v", err)
	}

	progressChan := make(chan worker.ProgressUpdate, 32)
	s.worker.NotifyWithProgress(doc.ID, progressChan)

	for update := range progressChan {
		if err := stream.Send(&mykbv1.IngestProgress{
			DocumentId:      update.DocumentID,
			Status:          update.Status,
			Message:         update.Message,
			ChunksTotal:     int32(update.ChunksTotal),
			ChunksProcessed: int32(update.ChunksProcessed),
		}); err != nil {
			log.Printf("server: failed to send progress for %s: %v", doc.ID, err)
			return err
		}
	}

	return nil
}

// IngestURLs inserts documents for the given URLs and streams batch progress
// updates as the worker processes them through the ingestion pipeline.
func (s *Server) IngestURLs(req *mykbv1.IngestURLsRequest, stream grpc.ServerStreamingServer[mykbv1.IngestURLsProgress]) error {
	ctx := stream.Context()
	urls := req.GetUrls()
	total := int32(len(urls))

	if total == 0 {
		return status.Errorf(codes.InvalidArgument, "no URLs provided")
	}

	type docInfo struct {
		id  string
		url string
	}
	var batchDocs []docInfo
	var current int32

	progressChans := make([]chan worker.ProgressUpdate, 0, len(urls))

	for _, url := range urls {
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

	// Queue all documents using blocking sends.
	for i, doc := range batchDocs {
		if err := s.worker.NotifyBlocking(ctx, doc.id, progressChans[i]); err != nil {
			return status.Errorf(codes.Internal, "queue document %s: %v", doc.url, err)
		}
	}

	// Stream progress from all documents sequentially.
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

// Query performs a hybrid search and returns matching chunk results.
func (s *Server) Query(ctx context.Context, req *mykbv1.QueryRequest) (*mykbv1.QueryResponse, error) {
	params := search.SearchParams{
		Query:       req.GetQuery(),
		TopK:        int(req.GetTopK()),
		VectorDepth: int(req.GetVectorDepth()),
		FTSDepth:    int(req.GetFtsDepth()),
		RerankDepth: int(req.GetRerankDepth()),
		NoMerge:     req.GetNoMerge(),
	}

	results, err := s.searcher.Search(ctx, params)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "search: %v", err)
	}

	protoResults := make([]*mykbv1.QueryResult, len(results))
	for i, r := range results {
		protoResults[i] = &mykbv1.QueryResult{
			ChunkId:       r.ChunkID,
			DocumentId:    r.DocumentID,
			ChunkIndex:    int32(r.ChunkIndex),
			ChunkIndexEnd: int32(r.ChunkIndexEnd),
			Score:         float32(r.Score),
			Text:          r.Text,
		}
	}

	return &mykbv1.QueryResponse{Results: protoResults}, nil
}

// ListDocuments returns a paginated list of documents.
func (s *Server) ListDocuments(ctx context.Context, req *mykbv1.ListDocumentsRequest) (*mykbv1.ListDocumentsResponse, error) {
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 50
	}
	offset := int(req.GetOffset())

	docs, total, err := s.pg.ListDocuments(ctx, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list documents: %v", err)
	}

	protoDocs := make([]*mykbv1.Document, len(docs))
	for i, doc := range docs {
		protoDocs[i] = documentToProto(doc)
	}

	return &mykbv1.ListDocumentsResponse{
		Documents: protoDocs,
		Total:     int32(total),
	}, nil
}

// GetDocuments retrieves documents by their IDs, optionally including content.
func (s *Server) GetDocuments(ctx context.Context, req *mykbv1.GetDocumentsRequest) (*mykbv1.GetDocumentsResponse, error) {
	docs, err := s.pg.GetDocumentsByIDs(ctx, req.GetIds())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get documents: %v", err)
	}

	protoDocs := make([]*mykbv1.Document, len(docs))
	for i, doc := range docs {
		d := documentToProto(doc)
		if req.GetIncludeContent() {
			content, err := s.fs.ReadDocument(doc.ID)
			if err != nil {
				log.Printf("server: failed to read content for document %s: %v", doc.ID, err)
			} else {
				d.Content = string(content)
			}
		}
		protoDocs[i] = d
	}

	return &mykbv1.GetDocumentsResponse{Documents: protoDocs}, nil
}

// DeleteDocument removes a document from all stores (Qdrant, Meilisearch,
// filesystem, and Postgres).
func (s *Server) DeleteDocument(ctx context.Context, req *mykbv1.DeleteDocumentRequest) (*mykbv1.DeleteDocumentResponse, error) {
	id := req.GetId()

	// Verify the document exists.
	if _, err := s.pg.GetDocument(ctx, id); err != nil {
		return nil, status.Errorf(codes.NotFound, "document not found: %v", err)
	}

	// Delete from Qdrant.
	if err := s.qdrant.DeleteByDocumentID(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "delete from qdrant: %v", err)
	}

	// Delete from Meilisearch.
	if err := s.meili.DeleteByDocumentID(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "delete from meilisearch: %v", err)
	}

	// Delete filesystem files.
	if err := s.fs.DeleteDocumentFiles(id); err != nil {
		return nil, status.Errorf(codes.Internal, "delete files: %v", err)
	}

	// Delete from Postgres (cascades to chunks).
	if err := s.pg.DeleteDocument(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "delete from postgres: %v", err)
	}

	return &mykbv1.DeleteDocumentResponse{}, nil
}

// documentToProto converts a storage.Document to a proto Document message.
func documentToProto(doc storage.Document) *mykbv1.Document {
	d := &mykbv1.Document{
		Id:        doc.ID,
		Url:       doc.URL,
		Status:    doc.Status,
		CreatedAt: doc.CreatedAt.Unix(),
		UpdatedAt: doc.UpdatedAt.Unix(),
	}
	if doc.Error != nil {
		d.Error = *doc.Error
	}
	if doc.Title != nil {
		d.Title = *doc.Title
	}
	if doc.ChunkCount != nil {
		d.ChunkCount = int32(*doc.ChunkCount)
	}
	if doc.CrawledAt != nil {
		d.CrawledAt = doc.CrawledAt.Unix()
	}
	return d
}
