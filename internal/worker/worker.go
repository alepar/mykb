package worker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"mykb/internal/config"
	"mykb/internal/pipeline"
	"mykb/internal/storage"
)

// ProgressUpdate is sent during document processing to report stage progress.
type ProgressUpdate struct {
	DocumentID      string
	Status          string
	Message         string
	ChunksTotal     int
	ChunksProcessed int
}

// workItem pairs a document ID with an optional progress channel.
type workItem struct {
	documentID string
	progress   chan<- ProgressUpdate
}

// Worker is the core pipeline orchestrator that processes documents through
// all ingestion stages: crawl, chunk, embed, and index.
type Worker struct {
	pg      *storage.PostgresStore
	fs      *storage.FilesystemStore
	crawler *pipeline.Crawler
	embedder *pipeline.Embedder
	indexer *pipeline.Indexer
	cfg     *config.Config
	notify  chan workItem
}

// NewWorker creates a Worker wired to all pipeline components.
func NewWorker(
	pg *storage.PostgresStore,
	fs *storage.FilesystemStore,
	crawler *pipeline.Crawler,
	embedder *pipeline.Embedder,
	indexer *pipeline.Indexer,
	cfg *config.Config,
) *Worker {
	return &Worker{
		pg:      pg,
		fs:      fs,
		crawler: crawler,
		embedder: embedder,
		indexer: indexer,
		cfg:     cfg,
		notify:  make(chan workItem, 8192),
	}
}

// Notify enqueues a document for processing without a progress channel.
// Non-blocking: if the channel is full the item is dropped (it will be
// picked up on next restart via GetPendingDocuments).
func (w *Worker) Notify(documentID string) {
	select {
	case w.notify <- workItem{documentID: documentID}:
	default:
		log.Printf("worker: notify channel full, dropping %s (will retry on restart)", documentID)
	}
}

// NotifyWithProgress enqueues a document for processing and attaches a
// progress channel that will receive updates as the document moves through
// pipeline stages.
func (w *Worker) NotifyWithProgress(documentID string, progress chan<- ProgressUpdate) {
	select {
	case w.notify <- workItem{documentID: documentID, progress: progress}:
	default:
		log.Printf("worker: notify channel full, dropping %s (will retry on restart)", documentID)
	}
}

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

func (w *Worker) Start(ctx context.Context) {
	// Resume interrupted docs by queuing them into the notify channel.
	// The batch coordinator below will process them in batches.
	docs, err := w.pg.GetPendingDocuments(ctx, w.cfg.MaxRetries)
	if err != nil {
		log.Printf("worker: failed to get pending documents: %v", err)
	}
	if len(docs) > 0 {
		log.Printf("worker: resuming %d pending documents", len(docs))
		for _, doc := range docs {
			select {
			case w.notify <- workItem{documentID: doc.ID}:
			case <-ctx.Done():
				return
			}
		}
	}

	// Launch periodic retry scanner in background.
	go w.retryScanner(ctx)

	// Launch batch coordinator.
	batchSize := w.cfg.WorkerConcurrency
	if batchSize < 1 {
		batchSize = 1
	}
	log.Printf("worker: starting batch coordinator (batch size %d)", batchSize)

	for {
		batch := w.pullBatch(ctx, batchSize)
		if len(batch) == 0 {
			return // context cancelled
		}

		w.processBatch(ctx, batch)
	}
}

// retryScanner periodically checks for documents whose next_retry_at has passed
// and queues them back into the notify channel for reprocessing.
func (w *Worker) retryScanner(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			docs, err := w.pg.GetPendingDocuments(ctx, w.cfg.MaxRetries)
			if err != nil {
				log.Printf("worker: retry scan failed: %v", err)
				continue
			}
			if len(docs) > 0 {
				log.Printf("worker: retry scan found %d documents to retry", len(docs))
				for _, doc := range docs {
					select {
					case w.notify <- workItem{documentID: doc.ID}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

// ProcessDocument fetches a document from Postgres and runs it through the
// pipeline stages, resuming from whatever status it is currently in.
// The progress channel may be nil if no listener is attached.
func (w *Worker) ProcessDocument(ctx context.Context, docID string, progress chan<- ProgressUpdate) error {
	doc, err := w.pg.GetDocument(ctx, docID)
	if err != nil {
		return fmt.Errorf("get document: %w", err)
	}

	// Clear error if retrying.
	if doc.Error != nil {
		if err := w.pg.ClearDocumentError(ctx, docID); err != nil {
			return fmt.Errorf("clear document error: %w", err)
		}
	}

	// vectors holds embedding results produced during doEmbed so doIndex
	// can use them without re-embedding. Keyed by chunk ID.
	vectors := make(map[string][]float32)

	// Resume from current status.
	switch doc.Status {
	case "PENDING":
		fallthrough
	case "CRAWLING":
		if err := w.doCrawl(ctx, &doc, progress); err != nil {
			return w.handleError(ctx, docID, err)
		}
		fallthrough
	case "CHUNKING":
		if err := w.doChunk(ctx, &doc, progress); err != nil {
			return w.handleError(ctx, docID, err)
		}
		fallthrough
	case "EMBEDDING":
		if err := w.doEmbed(ctx, &doc, progress, vectors); err != nil {
			return w.handleError(ctx, docID, err)
		}
		fallthrough
	case "INDEXING":
		if err := w.doIndex(ctx, &doc, progress, vectors); err != nil {
			return w.handleError(ctx, docID, err)
		}
	case "DONE":
		return nil
	}

	return nil
}

// doCrawl fetches the URL content via the crawler and stores the result.
func (w *Worker) doCrawl(ctx context.Context, doc *storage.Document, progress chan<- ProgressUpdate) error {
	if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "CRAWLING"); err != nil {
		return fmt.Errorf("set status CRAWLING: %w", err)
	}

	result, err := w.crawler.Crawl(ctx, doc.URL)
	if err != nil {
		return fmt.Errorf("crawl: %w", err)
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

// doChunk reads the document content, splits it into chunks, and stores them.
func (w *Worker) doChunk(ctx context.Context, doc *storage.Document, progress chan<- ProgressUpdate) error {
	if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "CHUNKING"); err != nil {
		return fmt.Errorf("set status CHUNKING: %w", err)
	}

	// Delete existing chunks on resume to avoid unique constraint violations.
	if err := w.pg.DeleteChunksByDocument(ctx, doc.ID); err != nil {
		return fmt.Errorf("delete existing chunks: %w", err)
	}

	content, err := w.fs.ReadDocument(doc.ID)
	if err != nil {
		return fmt.Errorf("read document: %w", err)
	}

	chunks := pipeline.ChunkMarkdown(string(content), pipeline.ChunkOptions{
		TargetTokens: w.cfg.ChunkTargetTokens,
		MaxTokens:    w.cfg.ChunkMaxTokens,
	})

	// Write each chunk to the filesystem.
	for i, chunkText := range chunks {
		if err := w.fs.WriteChunkText(doc.ID, i, []byte(chunkText)); err != nil {
			return fmt.Errorf("write chunk %d: %w", i, err)
		}
	}

	// Insert chunk records into Postgres.
	if _, err := w.pg.InsertChunks(ctx, doc.ID, len(chunks)); err != nil {
		return fmt.Errorf("insert chunks: %w", err)
	}

	if err := w.pg.SetDocumentChunkCount(ctx, doc.ID, len(chunks)); err != nil {
		return fmt.Errorf("set chunk count: %w", err)
	}

	sendProgress(progress, ProgressUpdate{
		DocumentID:  doc.ID,
		Status:      "CHUNKING",
		Message:     "Chunking complete",
		ChunksTotal: len(chunks),
	})

	return nil
}

// doEmbed reads all chunks, sends them together to the contextualized
// embedding API, and stores the resulting vectors.
func (w *Worker) doEmbed(ctx context.Context, doc *storage.Document, progress chan<- ProgressUpdate, vectors map[string][]float32) error {
	if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "EMBEDDING"); err != nil {
		return fmt.Errorf("set status EMBEDDING: %w", err)
	}

	chunks, err := w.pg.GetChunksByDocument(ctx, doc.ID)
	if err != nil {
		return fmt.Errorf("get chunks: %w", err)
	}

	if len(chunks) == 0 {
		sendProgress(progress, ProgressUpdate{
			DocumentID: doc.ID,
			Status:     "EMBEDDING",
			Message:    "Embedding complete (no chunks)",
		})
		return nil
	}

	// Read all chunk texts in order.
	texts := make([]string, len(chunks))
	for i, chunk := range chunks {
		chunkText, err := w.fs.ReadChunkText(doc.ID, chunk.ChunkIndex)
		if err != nil {
			return fmt.Errorf("read chunk %d text: %w", chunk.ChunkIndex, err)
		}
		texts[i] = string(chunkText)
	}

	// Embed all chunks together — the contextualized API uses sibling chunks
	// as mutual context for better embeddings.
	embeds, err := w.embedder.EmbedChunks(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed chunks: %w", err)
	}

	// Store vectors in memory and update chunk statuses.
	for i, chunk := range chunks {
		vectors[chunk.ID] = embeds[i]
		if err := w.pg.UpdateChunkStatus(ctx, chunk.ID, "EMBEDDED"); err != nil {
			return fmt.Errorf("update chunk %d status: %w", chunk.ChunkIndex, err)
		}
	}

	sendProgress(progress, ProgressUpdate{
		DocumentID:      doc.ID,
		Status:          "EMBEDDING",
		Message:         "Embedding complete",
		ChunksTotal:     len(chunks),
		ChunksProcessed: len(chunks),
	})

	return nil
}

// doIndex builds IndexableChunk entries and writes them to Qdrant and
// Meilisearch via the indexer. If vectors are not available in memory (resume
// scenario), the chunks are re-embedded first.
func (w *Worker) doIndex(ctx context.Context, doc *storage.Document, progress chan<- ProgressUpdate, vectors map[string][]float32) error {
	if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "INDEXING"); err != nil {
		return fmt.Errorf("set status INDEXING: %w", err)
	}

	chunks, err := w.pg.GetChunksByDocumentAndStatus(ctx, doc.ID, "EMBEDDED")
	if err != nil {
		return fmt.Errorf("get embedded chunks: %w", err)
	}

	if len(chunks) == 0 {
		// All chunks already indexed; just finalize.
		if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "DONE"); err != nil {
			return fmt.Errorf("set status DONE: %w", err)
		}
		sendProgress(progress, ProgressUpdate{
			DocumentID: doc.ID,
			Status:     "DONE",
			Message:    "Indexing complete",
		})
		return nil
	}

	// If vectors are missing (resume from crash), re-embed.
	needsReEmbed := false
	for _, chunk := range chunks {
		if _, ok := vectors[chunk.ID]; !ok {
			needsReEmbed = true
			break
		}
	}

	if needsReEmbed {
		texts := make([]string, len(chunks))
		for i, chunk := range chunks {
			chunkText, err := w.fs.ReadChunkText(doc.ID, chunk.ChunkIndex)
			if err != nil {
				return fmt.Errorf("read chunk %d text for re-embed: %w", chunk.ChunkIndex, err)
			}
			texts[i] = string(chunkText)
		}

		embeds, err := w.embedder.EmbedChunks(ctx, texts)
		if err != nil {
			return fmt.Errorf("re-embed chunks: %w", err)
		}
		for i, chunk := range chunks {
			vectors[chunk.ID] = embeds[i]
		}
	}

	// Build IndexableChunks.
	indexable := make([]pipeline.IndexableChunk, len(chunks))
	for i, chunk := range chunks {
		chunkText, err := w.fs.ReadChunkText(doc.ID, chunk.ChunkIndex)
		if err != nil {
			return fmt.Errorf("read chunk %d text for index: %w", chunk.ChunkIndex, err)
		}

		indexable[i] = pipeline.IndexableChunk{
			ID:                 chunk.ID,
			DocumentID:         doc.ID,
			ChunkIndex:         chunk.ChunkIndex,
			Vector:             vectors[chunk.ID],
			Text:       string(chunkText),
		}
	}

	if err := w.indexer.IndexChunks(ctx, indexable); err != nil {
		return fmt.Errorf("index chunks: %w", err)
	}

	// Update each chunk status to DONE.
	for _, chunk := range chunks {
		if err := w.pg.UpdateChunkStatus(ctx, chunk.ID, "DONE"); err != nil {
			return fmt.Errorf("update chunk %d status to DONE: %w", chunk.ChunkIndex, err)
		}
	}

	// Finalize document.
	if err := w.pg.UpdateDocumentStatus(ctx, doc.ID, "DONE"); err != nil {
		return fmt.Errorf("set status DONE: %w", err)
	}

	allChunks, err := w.pg.GetChunksByDocument(ctx, doc.ID)
	if err != nil {
		return fmt.Errorf("get all chunks: %w", err)
	}

	sendProgress(progress, ProgressUpdate{
		DocumentID:      doc.ID,
		Status:          "DONE",
		Message:         "Indexing complete",
		ChunksTotal:     len(allChunks),
		ChunksProcessed: len(allChunks),
	})

	return nil
}

// handleError records the error on the document and returns it.
func (w *Worker) handleError(ctx context.Context, docID string, err error) error {
	if setErr := w.pg.SetDocumentError(ctx, docID, err.Error()); setErr != nil {
		log.Printf("worker: failed to set error on document %s: %v", docID, setErr)
	}
	return err
}

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

	// Process crawl results: save + chunk for successes, handleError for failures.
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

// sendProgress sends an update on the channel if it is non-nil.
// Uses a non-blocking send to avoid stalling the worker if the consumer is slow.
func sendProgress(ch chan<- ProgressUpdate, update ProgressUpdate) {
	if ch == nil {
		return
	}
	select {
	case ch <- update:
	default:
		log.Printf("worker: progress channel full, dropping update for %s", update.DocumentID)
	}
}
