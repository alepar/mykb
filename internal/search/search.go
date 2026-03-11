package search

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"mykb/internal/config"
	"mykb/internal/pipeline"
	"mykb/internal/storage"
)

// HybridSearcher orchestrates hybrid search by fanning out to vector (Qdrant)
// and full-text (Meilisearch) backends, fusing results with RRF, and reranking.
type HybridSearcher struct {
	embedder    *pipeline.Embedder
	qdrant      *storage.QdrantStore
	meilisearch *storage.MeilisearchStore
	reranker    *Reranker
	fs          *storage.FilesystemStore
	cfg         *config.Config
}

// NewHybridSearcher creates a HybridSearcher with the given dependencies.
func NewHybridSearcher(
	embedder *pipeline.Embedder,
	qdrant *storage.QdrantStore,
	meili *storage.MeilisearchStore,
	reranker *Reranker,
	fs *storage.FilesystemStore,
	cfg *config.Config,
) *HybridSearcher {
	return &HybridSearcher{
		embedder:    embedder,
		qdrant:      qdrant,
		meilisearch: meili,
		reranker:    reranker,
		fs:          fs,
		cfg:         cfg,
	}
}

// SearchParams controls the behaviour of a hybrid search request.
type SearchParams struct {
	Query       string
	TopK        int
	VectorDepth int
	FTSDepth    int
	RerankDepth int
	NoMerge     bool
}

// SearchResult is a single result returned by Search.
type SearchResult struct {
	ChunkID       string
	DocumentID    string
	ChunkIndex    int
	ChunkIndexEnd int
	Score         float64
	Text          string
}

// chunkMeta holds the metadata we need to resolve a chunk ID back to its
// document and positional index.
type chunkMeta struct {
	DocumentID string
	ChunkIndex int
}

// applyDefaults fills zero-value fields in params with config defaults.
func applyDefaults(p SearchParams, cfg *config.Config) SearchParams {
	if p.TopK == 0 {
		p.TopK = cfg.DefaultTopK
	}
	if p.VectorDepth == 0 {
		p.VectorDepth = cfg.DefaultVectorDepth
	}
	if p.FTSDepth == 0 {
		p.FTSDepth = cfg.DefaultFTSDepth
	}
	if p.RerankDepth == 0 {
		p.RerankDepth = cfg.DefaultRerankDepth
	}
	return p
}

// Search performs a hybrid search: parallel vector + full-text fan-out, RRF
// fusion, filesystem chunk retrieval, and reranking.
func (h *HybridSearcher) Search(ctx context.Context, params SearchParams) ([]SearchResult, error) {
	params = applyDefaults(params, h.cfg)

	// --- parallel fan-out ---
	var (
		qdrantResults []storage.SearchResult
		meiliResults  []storage.MeiliHit
	)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		vec, err := h.embedder.EmbedQuery(gctx, params.Query)
		if err != nil {
			return fmt.Errorf("embed query: %w", err)
		}
		qdrantResults, err = h.qdrant.Search(gctx, vec, uint64(params.VectorDepth))
		if err != nil {
			return fmt.Errorf("qdrant search: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		meiliResults, err = h.meilisearch.Search(gctx, params.Query, int64(params.FTSDepth))
		if err != nil {
			return fmt.Errorf("meilisearch search: %w", err)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// --- convert to ScoredID lists ---
	qdrantList := make([]ScoredID, len(qdrantResults))
	for i, r := range qdrantResults {
		qdrantList[i] = ScoredID{ID: r.ID, Score: float64(r.Score)}
	}

	meiliList := make([]ScoredID, len(meiliResults))
	for i, r := range meiliResults {
		meiliList[i] = ScoredID{ID: r.ChunkID, Score: r.RankingScore}
	}

	// --- RRF fusion ---
	fused := RRF(h.cfg.RRFConstant, params.RerankDepth, qdrantList, meiliList)
	if len(fused) == 0 {
		return nil, nil
	}

	// --- build chunk metadata lookup ---
	metaMap := make(map[string]chunkMeta, len(qdrantResults)+len(meiliResults))
	for _, r := range qdrantResults {
		docID, _ := r.Payload["document_id"].(string)
		chunkIdx := 0
		if v, ok := r.Payload["chunk_index"]; ok {
			switch ci := v.(type) {
			case int64:
				chunkIdx = int(ci)
			case float64:
				chunkIdx = int(ci)
			case int:
				chunkIdx = ci
			}
		}
		metaMap[r.ID] = chunkMeta{DocumentID: docID, ChunkIndex: chunkIdx}
	}
	for _, r := range meiliResults {
		if _, exists := metaMap[r.ChunkID]; !exists {
			metaMap[r.ChunkID] = chunkMeta{DocumentID: r.DocumentID, ChunkIndex: r.ChunkIndex}
		}
	}

	// --- prepare reranker input ---
	candidateIDs := make([]string, len(fused))
	chunkTexts := make([]string, len(fused))
	for i, f := range fused {
		candidateIDs[i] = f.ID
		meta, ok := metaMap[f.ID]
		if !ok {
			continue
		}
		text, err := h.fs.ReadChunkText(meta.DocumentID, meta.ChunkIndex)
		if err != nil {
			return nil, fmt.Errorf("read chunk text for %s: %w", f.ID, err)
		}
		chunkTexts[i] = string(text)
	}

	// --- rerank ---
	reranked, err := h.reranker.Rerank(ctx, params.Query, chunkTexts, params.TopK)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}

	// --- RSE or individual results ---
	if params.NoMerge {
		results := make([]SearchResult, len(reranked))
		for i, rr := range reranked {
			id := candidateIDs[rr.Index]
			meta := metaMap[id]
			results[i] = SearchResult{
				ChunkID:       id,
				DocumentID:    meta.DocumentID,
				ChunkIndex:    meta.ChunkIndex,
				ChunkIndexEnd: meta.ChunkIndex + 1,
				Score:         rr.Score,
				Text:          chunkTexts[rr.Index],
			}
		}
		return results, nil
	}

	// Build RSE input from reranked results.
	rankedChunks := make([]RankedChunk, len(reranked))
	for i, rr := range reranked {
		id := candidateIDs[rr.Index]
		meta := metaMap[id]
		rankedChunks[i] = RankedChunk{
			DocumentID: meta.DocumentID,
			ChunkIndex: meta.ChunkIndex,
			Rank:       i,
			Score:      rr.Score,
		}
	}

	rseParams := RSEParams{
		MaxLength:              h.cfg.RSEMaxLength,
		OverallMaxLength:       h.cfg.RSEOverallMaxLength,
		MinimumValue:           h.cfg.RSEMinimumValue,
		IrrelevantChunkPenalty: h.cfg.RSEIrrelevantChunkPenalty,
		DecayRate:              h.cfg.RSEDecayRate,
	}
	segments := ExtractSegments(rankedChunks, params.TopK, rseParams)

	results := make([]SearchResult, len(segments))
	for i, seg := range segments {
		var merged strings.Builder
		for ci := seg.StartChunk; ci < seg.EndChunk; ci++ {
			text, err := h.fs.ReadChunkText(seg.DocumentID, ci)
			if err != nil {
				return nil, fmt.Errorf("read chunk text for %s chunk %d: %w", seg.DocumentID, ci, err)
			}
			if merged.Len() > 0 {
				merged.WriteString("\n\n")
			}
			merged.Write(text)
		}
		results[i] = SearchResult{
			DocumentID:    seg.DocumentID,
			ChunkIndex:    seg.StartChunk,
			ChunkIndexEnd: seg.EndChunk,
			Score:         seg.Score,
			Text:          merged.String(),
		}
	}

	return results, nil
}
