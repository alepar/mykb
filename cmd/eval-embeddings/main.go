package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"time"
)

const (
	voyageAPI      = "https://api.voyageai.com/v1"
	stdModel       = "voyage-4-large"
	ctxModel       = "voyage-context-3"
	dimension      = 1024
	docID          = "99e95dae-46e4-4cf5-81ac-d5f330fdcf32"
	dataDir        = "/tmp/mykb-data/documents/99/e9"
)

var queries = []struct {
	query       string
	expectChunk int // which chunk index should rank highest
}{
	{"how does defer work in Go", 2},
	{"what happens when panic is called", 3},
	{"recover from a panic in Go", 3},
	{"closing files with defer", 2},
	{"real world example of panic and recover", 4},
}

// --- Voyage AI request/response types ---

type stdEmbedRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model"`
	InputType  string   `json:"input_type"`
	Dimensions int      `json:"output_dimension,omitempty"`
}

type stdEmbedResponse struct {
	Data  []stdEmbedData `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

type stdEmbedData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type ctxEmbedRequest struct {
	Inputs    [][]string `json:"inputs"`
	Model     string     `json:"model"`
	InputType string     `json:"input_type"`
	Dimension int        `json:"output_dimension,omitempty"`
}

// The REST API returns a nested structure:
// {"object":"list","data":[{"object":"list","data":[{"object":"embedding","embedding":[...],"index":0}, ...]}]}
type ctxEmbedResponse struct {
	Data        []ctxEmbedGroup `json:"data"`
	TotalTokens int             `json:"total_tokens"`
}

type ctxEmbedGroup struct {
	Data []ctxEmbedItem `json:"data"`
}

type ctxEmbedItem struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

func main() {
	apiKey := os.Getenv("VOYAGE_API_KEY")
	if apiKey == "" {
		log.Fatal("VOYAGE_API_KEY not set")
	}

	// Read document and chunks
	docBytes, err := os.ReadFile(fmt.Sprintf("%s/%s.md", dataDir, docID))
	if err != nil {
		log.Fatalf("read document: %v", err)
	}
	document := string(docBytes)

	var chunks []string
	for i := 0; ; i++ {
		path := fmt.Sprintf("%s/%s.%03dt.md", dataDir, docID, i)
		data, err := os.ReadFile(path)
		if err != nil {
			break
		}
		chunks = append(chunks, string(data))
	}
	fmt.Printf("Document: %d bytes, %d chunks\n\n", len(document), len(chunks))

	// Print chunk previews
	for i, c := range chunks {
		preview := c
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}
		fmt.Printf("  Chunk %d: %s\n", i, preview)
	}
	fmt.Println()

	// --- Standard embeddings (voyage-4-large) ---
	fmt.Println("=== voyage-4-large (standard, chunk-only) ===")
	stdChunkEmbeds := embedStandard(apiKey, chunks, "document")

	// --- Contextualized embeddings (voyage-context-3) ---
	fmt.Println("=== voyage-context-3 (contextualized, with document) ===")
	ctxChunkEmbeds := embedContextualized(apiKey, document, chunks)

	// --- Run queries ---
	fmt.Println("\n=== RESULTS ===")
	fmt.Println()

	stdWins, ctxWins, ties := 0, 0, 0

	for _, q := range queries {
		fmt.Printf("Query: %q (expected: chunk %d)\n", q.query, q.expectChunk)

		// Embed query with both models
		stdQueryEmbed := embedStandard(apiKey, []string{q.query}, "query")[0]
		ctxQueryEmbed := embedContextualizedQuery(apiKey, q.query)

		// Rank chunks by cosine similarity
		stdRanking := rankBySimilarity(stdChunkEmbeds, stdQueryEmbed)
		ctxRanking := rankBySimilarity(ctxChunkEmbeds, ctxQueryEmbed)

		fmt.Printf("  %-20s  %-20s\n", "voyage-4-large", "voyage-context-3")
		for rank := 0; rank < len(chunks); rank++ {
			stdMark := ""
			ctxMark := ""
			if stdRanking[rank].index == q.expectChunk {
				stdMark = " <--"
			}
			if ctxRanking[rank].index == q.expectChunk {
				ctxMark = " <--"
			}
			fmt.Printf("  #%d chunk %d (%.4f)%s    #%d chunk %d (%.4f)%s\n",
				rank+1, stdRanking[rank].index, stdRanking[rank].score, stdMark,
				rank+1, ctxRanking[rank].index, ctxRanking[rank].score, ctxMark)
		}

		stdPos := positionOf(stdRanking, q.expectChunk)
		ctxPos := positionOf(ctxRanking, q.expectChunk)
		if stdPos < ctxPos {
			stdWins++
			fmt.Printf("  Winner: voyage-4-large (rank %d vs %d)\n", stdPos+1, ctxPos+1)
		} else if ctxPos < stdPos {
			ctxWins++
			fmt.Printf("  Winner: voyage-context-3 (rank %d vs %d)\n", ctxPos+1, stdPos+1)
		} else {
			ties++
			fmt.Printf("  Tie (both rank %d)\n", stdPos+1)
		}
		fmt.Println()
	}

	fmt.Println("=== SUMMARY ===")
	fmt.Printf("voyage-4-large wins:  %d\n", stdWins)
	fmt.Printf("voyage-context-3 wins: %d\n", ctxWins)
	fmt.Printf("ties:                  %d\n", ties)
}

type ranked struct {
	index int
	score float64
}

func rankBySimilarity(embeddings [][]float64, query []float64) []ranked {
	results := make([]ranked, len(embeddings))
	for i, emb := range embeddings {
		results[i] = ranked{index: i, score: cosine(emb, query)}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})
	return results
}

func positionOf(ranking []ranked, chunkIndex int) int {
	for i, r := range ranking {
		if r.index == chunkIndex {
			return i
		}
	}
	return len(ranking)
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// --- API callers ---

func embedStandard(apiKey string, texts []string, inputType string) [][]float64 {
	reqBody, _ := json.Marshal(stdEmbedRequest{
		Input:      texts,
		Model:      stdModel,
		InputType:  inputType,
		Dimensions: dimension,
	})

	body := voyagePost(apiKey, "/embeddings", reqBody)
	var resp stdEmbedResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Fatalf("decode standard embed response: %v\nraw: %s", err, string(body[:min(500, len(body))]))
	}
	fmt.Printf("  tokens: %d, embeddings: %d\n", resp.Usage.TotalTokens, len(resp.Data))

	embeddings := make([][]float64, len(resp.Data))
	for _, d := range resp.Data {
		embeddings[d.Index] = d.Embedding
	}
	return embeddings
}

func embedContextualized(apiKey string, document string, chunks []string) [][]float64 {
	// All chunks from the same document go in one inner list.
	// The model uses sibling chunks as mutual context — no separate document element.
	// We do NOT include the full document; only the chunks in document order.
	_ = document // not used — context comes from sibling chunks

	reqBody, _ := json.Marshal(ctxEmbedRequest{
		Inputs:    [][]string{chunks},
		Model:     ctxModel,
		InputType: "document",
		Dimension: dimension,
	})

	body := voyagePost(apiKey, "/contextualizedembeddings", reqBody)
	if len(body) < 500 {
		fmt.Printf("  raw response: %s\n", string(body))
	}
	var resp ctxEmbedResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Fatalf("decode contextualized embed response: %v\nraw: %s", err, string(body[:min(500, len(body))]))
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Data) == 0 {
		log.Fatalf("contextualized embed returned no results\nraw: %s", string(body[:min(500, len(body))]))
	}
	embeddings := make([][]float64, len(resp.Data[0].Data))
	for _, item := range resp.Data[0].Data {
		embeddings[item.Index] = item.Embedding
	}
	fmt.Printf("  tokens: %d, embeddings: %d\n", resp.TotalTokens, len(embeddings))

	return embeddings
}

func embedContextualizedQuery(apiKey string, query string) []float64 {
	reqBody, _ := json.Marshal(ctxEmbedRequest{
		Inputs:    [][]string{{query}},
		Model:     ctxModel,
		InputType: "query",
		Dimension: dimension,
	})

	body := voyagePost(apiKey, "/contextualizedembeddings", reqBody)
	var resp ctxEmbedResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Fatalf("decode contextualized query embed response: %v", err)
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Data) == 0 {
		log.Fatal("contextualized query embed returned no embeddings")
	}
	return resp.Data[0].Data[0].Embedding
}

func voyagePost(apiKey, path string, reqBody []byte) []byte {
	req, err := http.NewRequest(http.MethodPost, voyageAPI+path, bytes.NewReader(reqBody))
	if err != nil {
		log.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("voyage API call to %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("voyage API %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return body
}
