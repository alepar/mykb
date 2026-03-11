package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/austinfhunter/voyageai"
)

// Embedder wraps a Voyage AI client for computing text embeddings.
type Embedder struct {
	client    *voyageai.VoyageClient
	model     string
	batchSize int
}

// NewEmbedder creates an Embedder that calls Voyage AI with the given model.
// batchSize controls how many texts are sent per API call (max 128 for Voyage).
func NewEmbedder(apiKey, model string, batchSize int) *Embedder {
	if batchSize <= 0 {
		batchSize = 128
	}
	client := voyageai.NewClient(&voyageai.VoyageClientOpts{
		Key:        apiKey,
		TimeOut:    int((1 * time.Minute).Milliseconds()),
		MaxRetries: 5,
	})
	return &Embedder{
		client:    client,
		model:     model,
		batchSize: batchSize,
	}
}

// EmbedDocuments computes embeddings for a slice of texts using input_type="document".
// Texts are processed in batches of batchSize. The returned vectors are in the
// same order as the input texts.
func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	inputType := "document"
	opts := &voyageai.EmbeddingRequestOpts{InputType: &inputType}

	var all [][]float32
	for start := 0; start < len(texts); start += e.batchSize {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("embed documents cancelled: %w", err)
		}
		end := start + e.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		resp, err := e.client.Embed(batch, e.model, opts)
		if err != nil {
			return nil, fmt.Errorf("embed batch [%d:%d] failed: %w", start, end, err)
		}
		for _, d := range resp.Data {
			all = append(all, d.Embedding)
		}
	}
	return all, nil
}

// EmbedQuery computes an embedding for a single query string using input_type="query".
func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("embed query cancelled: %w", err)
	}
	inputType := "query"
	opts := &voyageai.EmbeddingRequestOpts{InputType: &inputType}
	resp, err := e.client.Embed([]string{text}, e.model, opts)
	if err != nil {
		return nil, fmt.Errorf("embed query failed: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("embed query returned no data")
	}
	return resp.Data[0].Embedding, nil
}
