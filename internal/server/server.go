package server

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	mykbv1 "mykb/gen/mykb/v1"
	"mykb/internal/config"
	"mykb/internal/pipeline"
	"mykb/internal/search"
	"mykb/internal/storage"
	"mykb/internal/wiki"
	"mykb/internal/worker"
)

// wikiNameRegexp validates that a wiki name only contains safe characters,
// preventing LIKE injection in Postgres queries.
var wikiNameRegexp = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validWikiName(s string) bool {
	return wikiNameRegexp.MatchString(s)
}

// Server implements the mykbv1connect.KBServiceHandler interface.
type Server struct {
	pg           *storage.PostgresStore
	fs           *storage.FilesystemStore
	qdrant       *storage.QdrantStore
	meili        *storage.MeilisearchStore
	searcher     *search.HybridSearcher
	worker       *worker.Worker
	cfg          *config.Config
	wikiIngestor *pipeline.WikiIngestor
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
	wikiIngestor *pipeline.WikiIngestor,
) *Server {
	return &Server{
		pg:           pg,
		fs:           fs,
		qdrant:       qdrant,
		meili:        meili,
		searcher:     searcher,
		worker:       w,
		cfg:          cfg,
		wikiIngestor: wikiIngestor,
	}
}

// IngestURL inserts a document for the given URL and streams progress updates
// as the worker processes it through the ingestion pipeline.
func (s *Server) IngestURL(ctx context.Context, req *connect.Request[mykbv1.IngestURLRequest], stream *connect.ServerStream[mykbv1.IngestProgress]) error {
	// If force is set, delete the existing document first.
	if req.Msg.GetForce() {
		existing, err := s.pg.GetDocumentByURL(ctx, req.Msg.GetUrl())
		if err == nil && existing.ID != "" {
			if err := s.deleteDocument(ctx, existing.ID); err != nil {
				log.Printf("server: force delete of existing document %s failed: %v", existing.ID, err)
			}
		}
	}

	doc, err := s.pg.InsertDocument(ctx, req.Msg.GetUrl())
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("URL already ingested: %s", req.Msg.GetUrl()))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("insert document: %v", err))
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
func (s *Server) IngestURLs(ctx context.Context, req *connect.Request[mykbv1.IngestURLsRequest], stream *connect.ServerStream[mykbv1.IngestURLsProgress]) error {
	urls := req.Msg.GetUrls()
	total := int32(len(urls))

	if total == 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no URLs provided"))
	}

	type docInfo struct {
		id  string
		url string
	}
	var batchDocs []docInfo
	var current int32

	progressChans := make([]chan worker.ProgressUpdate, 0, len(urls))

	for _, url := range urls {
		if req.Msg.GetForce() {
			existing, err := s.pg.GetDocumentByURL(ctx, url)
			if err == nil && existing.ID != "" {
				if err := s.deleteDocument(ctx, existing.ID); err != nil {
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
			return connect.NewError(connect.CodeInternal, fmt.Errorf("insert document for %s: %v", url, err))
		}

		ch := make(chan worker.ProgressUpdate, 32)
		progressChans = append(progressChans, ch)
		batchDocs = append(batchDocs, docInfo{id: doc.ID, url: url})
	}

	// Queue all documents using blocking sends.
	for i, doc := range batchDocs {
		if err := s.worker.NotifyBlocking(ctx, doc.id, progressChans[i]); err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("queue document %s: %v", doc.url, err))
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
func (s *Server) Query(ctx context.Context, req *connect.Request[mykbv1.QueryRequest]) (*connect.Response[mykbv1.QueryResponse], error) {
	params := search.SearchParams{
		Query:       req.Msg.GetQuery(),
		TopK:        int(req.Msg.GetTopK()),
		VectorDepth: int(req.Msg.GetVectorDepth()),
		FTSDepth:    int(req.Msg.GetFtsDepth()),
		RerankDepth: int(req.Msg.GetRerankDepth()),
		NoMerge:     req.Msg.GetNoMerge(),
	}

	results, err := s.searcher.Search(ctx, params)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("search: %v", err))
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

	return connect.NewResponse(&mykbv1.QueryResponse{Results: protoResults}), nil
}

// ListDocuments returns a paginated list of documents.
func (s *Server) ListDocuments(ctx context.Context, req *connect.Request[mykbv1.ListDocumentsRequest]) (*connect.Response[mykbv1.ListDocumentsResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit == 0 {
		limit = 50
	}
	offset := int(req.Msg.GetOffset())

	docs, total, err := s.pg.ListDocuments(ctx, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list documents: %v", err))
	}

	protoDocs := make([]*mykbv1.Document, len(docs))
	for i, doc := range docs {
		protoDocs[i] = documentToProto(doc)
	}

	return connect.NewResponse(&mykbv1.ListDocumentsResponse{
		Documents: protoDocs,
		Total:     int32(total),
	}), nil
}

// GetDocuments retrieves documents by their IDs, optionally including content.
func (s *Server) GetDocuments(ctx context.Context, req *connect.Request[mykbv1.GetDocumentsRequest]) (*connect.Response[mykbv1.GetDocumentsResponse], error) {
	docs, err := s.pg.GetDocumentsByIDs(ctx, req.Msg.GetIds())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get documents: %v", err))
	}

	protoDocs := make([]*mykbv1.Document, len(docs))
	for i, doc := range docs {
		d := documentToProto(doc)
		if req.Msg.GetIncludeContent() {
			content, err := s.fs.ReadDocument(doc.ID)
			if err != nil {
				log.Printf("server: failed to read content for document %s: %v", doc.ID, err)
			} else {
				d.Content = string(content)
			}
		}
		protoDocs[i] = d
	}

	return connect.NewResponse(&mykbv1.GetDocumentsResponse{Documents: protoDocs}), nil
}

// DeleteDocument removes a document from all stores (Qdrant, Meilisearch,
// filesystem, and Postgres).
func (s *Server) DeleteDocument(ctx context.Context, req *connect.Request[mykbv1.DeleteDocumentRequest]) (*connect.Response[mykbv1.DeleteDocumentResponse], error) {
	if err := s.deleteDocument(ctx, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&mykbv1.DeleteDocumentResponse{}), nil
}

// deleteDocument contains the actual delete logic: verify exists, then delete
// from all stores. Used by both DeleteDocument and force-delete paths in
// IngestURL/IngestURLs.
func (s *Server) deleteDocument(ctx context.Context, id string) error {
	// Verify the document exists.
	if _, err := s.pg.GetDocument(ctx, id); err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("document not found: %v", err))
	}

	// Delete from Qdrant.
	if err := s.qdrant.DeleteByDocumentID(ctx, id); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("delete from qdrant: %v", err))
	}

	// Delete from Meilisearch.
	if err := s.meili.DeleteByDocumentID(ctx, id); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("delete from meilisearch: %v", err))
	}

	// Delete filesystem files.
	if err := s.fs.DeleteDocumentFiles(id); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("delete files: %v", err))
	}

	// Delete from Postgres (cascades to chunks).
	if err := s.pg.DeleteDocument(ctx, id); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("delete from postgres: %v", err))
	}

	return nil
}

// IngestMarkdown ingests a single wiki document synchronously. The body
// includes frontmatter; the pipeline strips it before chunking. Idempotent
// on (url, content_hash).
func (s *Server) IngestMarkdown(ctx context.Context, req *connect.Request[mykbv1.IngestMarkdownRequest]) (*connect.Response[mykbv1.IngestMarkdownResponse], error) {
	url := req.Msg.GetUrl()
	if !wiki.IsWikiURL(url) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("url must use wiki:// scheme: %q", url))
	}
	// Validate the wiki name extracted from the URL — guards against LIKE injection
	// downstream and ensures URLs round-trip cleanly.
	wikiName, vaultPath, err := wiki.URLToVaultPath(url)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("malformed wiki url: %v", err))
	}
	if !validWikiName(wikiName) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid wiki name %q (must match [a-zA-Z0-9_-]+)", wikiName))
	}

	body := req.Msg.GetBody()
	hash := req.Msg.GetContentHash()
	if hash == "" {
		hash = pipeline.ComputeContentHash(body)
	}

	title := req.Msg.GetTitle()
	if title == "" {
		title = wiki.ExtractTitle(body, vaultPath)
	}

	res, err := s.wikiIngestor.Ingest(ctx, url, title, body, hash)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&mykbv1.IngestMarkdownResponse{
		DocumentId: res.DocumentID,
		Chunks:     int32(res.Chunks),
		WasNoop:    res.WasNoop,
	}), nil
}

// ListWikiDocuments returns (id, url, content_hash) for all wiki documents
// belonging to the given wiki. Used by `mykb wiki sync` for diffing.
func (s *Server) ListWikiDocuments(ctx context.Context, req *connect.Request[mykbv1.ListWikiDocumentsRequest]) (*connect.Response[mykbv1.ListWikiDocumentsResponse], error) {
	wikiName := req.Msg.GetWikiName()
	if wikiName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("wiki_name is required"))
	}
	if !validWikiName(wikiName) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid wiki name %q (must match [a-zA-Z0-9_-]+)", wikiName))
	}
	docs, err := s.pg.ListWikiDocuments(ctx, wikiName)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list wiki documents: %v", err))
	}
	out := make([]*mykbv1.WikiDocument, 0, len(docs))
	for _, d := range docs {
		out = append(out, &mykbv1.WikiDocument{
			Id:          d.ID,
			Url:         d.URL,
			ContentHash: d.ContentHash,
		})
	}
	return connect.NewResponse(&mykbv1.ListWikiDocumentsResponse{Documents: out}), nil
}

// documentToProto converts a storage.Document to a proto Document message.
func documentToProto(doc storage.Document) *mykbv1.Document {
	d := &mykbv1.Document{
		Id:        doc.ID,
		Url:       doc.URL,
		Status:    doc.DisplayStatus(),
		Step:      doc.Step,
		State:     doc.State,
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
