package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"mykb/internal/ratelimit"
)

const voyageBaseURL = "https://api.voyageai.com/v1"

// Embedder wraps the Voyage AI API for computing text embeddings.
// It uses the contextualized embeddings endpoint (voyage-context-3) which
// encodes chunks with awareness of their sibling chunks in the same document.
type Embedder struct {
	apiKey     string
	model      string
	dimension  int
	httpClient *http.Client
	baseURL    string // overridable for tests
	limiter    *ratelimit.AdaptiveLimiter
}

// NewEmbedder creates an Embedder that calls Voyage AI with the given model.
func NewEmbedder(apiKey, model string, dimension int) *Embedder {
	return &Embedder{
		apiKey:    apiKey,
		model:     model,
		dimension: dimension,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
		baseURL: voyageBaseURL,
	}
}

func (e *Embedder) SetLimiter(l *ratelimit.AdaptiveLimiter) {
	e.limiter = l
}

// --- request/response types for contextualized embeddings ---

type ctxEmbedRequest struct {
	Inputs          [][]string `json:"inputs"`
	Model           string     `json:"model"`
	InputType       string     `json:"input_type,omitempty"`
	OutputDimension int        `json:"output_dimension,omitempty"`
	OutputDtype     string     `json:"output_dtype,omitempty"`
}

type ctxEmbedResponse struct {
	Data  []ctxEmbedGroup `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

type ctxEmbedGroup struct {
	Data  []ctxEmbedItem `json:"data"`
	Index int            `json:"index"`
}

type ctxEmbedItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

const (
	embedMaxRetries = 5
	embedBaseDelay  = 4 * time.Second
)

func (e *Embedder) embedWithRetry(ctx context.Context, inputs [][]string, inputType string, expectedCount int) (*ctxEmbedResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= embedMaxRetries; attempt++ {
		if attempt > 0 {
			delay := embedBaseDelay * time.Duration(1<<(attempt-1))
			log.Printf("embed: retry %d/%d after %v", attempt, embedMaxRetries, delay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		if e.limiter != nil {
			e.limiter.Acquire()
		}

		resp, err := e.postContextualized(ctx, inputs, inputType)
		if err != nil {
			lastErr = err
			if e.limiter != nil {
				e.limiter.ReportFailure()
			}
			continue
		}

		// Validate response
		if len(resp.Data) == 0 || len(resp.Data[0].Data) != expectedCount {
			got := 0
			if len(resp.Data) > 0 {
				got = len(resp.Data[0].Data)
			}
			lastErr = fmt.Errorf("embedding response size mismatch: got %d, expected %d",
				got, expectedCount)
			if e.limiter != nil {
				e.limiter.ReportFailure()
			}
			continue
		}

		if e.limiter != nil {
			e.limiter.ReportSuccess()
		}
		return resp, nil
	}
	return nil, fmt.Errorf("embed failed after %d retries: %w", embedMaxRetries, lastErr)
}

// EmbedChunks computes contextualized embeddings for all chunks of a single
// document. Chunks are sent together so the model encodes each chunk with
// awareness of its siblings. Returns one vector per chunk in order.
func (e *Embedder) EmbedChunks(ctx context.Context, chunks []string) ([][]float32, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	resp, err := e.embedWithRetry(ctx, [][]string{chunks}, "document", len(chunks))
	if err != nil {
		return nil, fmt.Errorf("embed chunks: %w", err)
	}

	log.Printf("embed [%s]: chunks=%d tokens=%d dims=%d",
		e.model, len(chunks), resp.Usage.TotalTokens, len(resp.Data[0].Data[0].Embedding))

	embeddings := make([][]float32, len(resp.Data[0].Data))
	for _, item := range resp.Data[0].Data {
		embeddings[item.Index] = item.Embedding
	}
	return embeddings, nil
}

// EmbedQuery computes an embedding for a single query string.
func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	resp, err := e.postContextualized(ctx, [][]string{{text}}, "query")
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	if len(resp.Data) == 0 || len(resp.Data[0].Data) == 0 {
		return nil, fmt.Errorf("embed query: empty response")
	}

	return resp.Data[0].Data[0].Embedding, nil
}

func (e *Embedder) postContextualized(ctx context.Context, inputs [][]string, inputType string) (*ctxEmbedResponse, error) {
	reqBody, err := json.Marshal(ctxEmbedRequest{
		Inputs:          inputs,
		Model:           e.model,
		InputType:       inputType,
		OutputDimension: e.dimension,
		OutputDtype:     "int8",
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/contextualizedembeddings", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("voyage API returned %d: %s", resp.StatusCode, string(body))
	}

	var result ctxEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
