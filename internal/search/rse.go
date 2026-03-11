package search

import (
	"math"
	"sort"
)

// RSEParams controls the Relevant Segment Extraction algorithm.
type RSEParams struct {
	MaxLength              int     // max chunks in a single segment
	OverallMaxLength       int     // max total chunks across all segments
	MinimumValue           float64 // minimum segment score to include
	IrrelevantChunkPenalty float64 // penalty for unchunked positions
	DecayRate              float64 // exponential decay rate for rank
}

// DefaultRSEParams returns "balanced" preset defaults.
func DefaultRSEParams() RSEParams {
	return RSEParams{
		MaxLength:              15,
		OverallMaxLength:       30,
		MinimumValue:           0.5,
		IrrelevantChunkPenalty: 0.18,
		DecayRate:              30,
	}
}

// RankedChunk is a reranked chunk with its document context.
type RankedChunk struct {
	DocumentID string
	ChunkIndex int
	Rank       int     // 0-based rank from reranker
	Score      float64 // reranker relevance score
}

// Segment is a contiguous range of chunks from a single document.
type Segment struct {
	DocumentID string
	StartChunk int     // inclusive
	EndChunk   int     // exclusive
	Score      float64 // sum of relevance values
}

// relevanceValue computes the value of a single chunk position.
func relevanceValue(rank int, score, decayRate, penalty float64) float64 {
	return math.Exp(float64(-rank)/decayRate)*score - penalty
}

// ExtractSegments implements the RSE algorithm: given reranked chunks, it
// builds a meta-document per document, scores each position, and greedily
// selects the best non-overlapping contiguous segments.
func ExtractSegments(chunks []RankedChunk, topKPerDoc int, params RSEParams) []Segment {
	if len(chunks) == 0 {
		return nil
	}

	// Group chunks by document and find max chunk index per doc.
	type docInfo struct {
		maxIndex int
		chunks   map[int]RankedChunk // chunkIndex -> RankedChunk
	}
	docs := make(map[string]*docInfo)
	var docOrder []string

	for _, c := range chunks {
		di, ok := docs[c.DocumentID]
		if !ok {
			di = &docInfo{chunks: make(map[int]RankedChunk)}
			docs[c.DocumentID] = di
			docOrder = append(docOrder, c.DocumentID)
		}
		di.chunks[c.ChunkIndex] = c
		if c.ChunkIndex > di.maxIndex {
			di.maxIndex = c.ChunkIndex
		}
	}

	// Build relevance values per document.
	type docPositions struct {
		docID  string
		values []float64
	}

	var allDocs []docPositions
	for _, docID := range docOrder {
		di := docs[docID]
		n := di.maxIndex + 1
		vals := make([]float64, n)
		for i := range vals {
			vals[i] = -params.IrrelevantChunkPenalty
		}
		for idx, c := range di.chunks {
			vals[idx] = relevanceValue(c.Rank, c.Score, params.DecayRate, params.IrrelevantChunkPenalty)
		}
		allDocs = append(allDocs, docPositions{docID: docID, values: vals})
	}

	// Greedy segment selection across all documents.
	var segments []Segment
	totalLength := 0

	covered := make([][]bool, len(allDocs))
	for i, dp := range allDocs {
		covered[i] = make([]bool, len(dp.values))
	}

	for totalLength < params.OverallMaxLength {
		bestScore := math.Inf(-1)
		bestDocIdx := -1
		bestStart := -1
		bestEnd := -1

		for di, dp := range allDocs {
			n := len(dp.values)
			for start := 0; start < n; start++ {
				if covered[di][start] {
					continue
				}
				if dp.values[start] <= 0 {
					continue
				}

				sum := 0.0
				for end := start + 1; end <= start+params.MaxLength && end <= n; end++ {
					if covered[di][end-1] {
						break
					}
					sum += dp.values[end-1]

					segLen := end - start
					if totalLength+segLen > params.OverallMaxLength {
						break
					}

					if sum > bestScore {
						bestScore = sum
						bestDocIdx = di
						bestStart = start
						bestEnd = end
					}
				}
			}
		}

		if bestDocIdx < 0 || bestScore < params.MinimumValue {
			break
		}

		segments = append(segments, Segment{
			DocumentID: allDocs[bestDocIdx].docID,
			StartChunk: bestStart,
			EndChunk:   bestEnd,
			Score:      bestScore,
		})
		totalLength += bestEnd - bestStart

		for i := bestStart; i < bestEnd; i++ {
			covered[bestDocIdx][i] = true
		}
	}

	sort.Slice(segments, func(i, j int) bool {
		return segments[i].Score > segments[j].Score
	})

	return segments
}
