package search

import (
	"math"
	"testing"
)

func TestExtractSegments_SingleChunkAboveThreshold(t *testing.T) {
	chunks := []RankedChunk{
		{DocumentID: "doc1", ChunkIndex: 3, Rank: 0, Score: 0.9},
	}
	params := DefaultRSEParams()
	segments := ExtractSegments(chunks, 10, params)

	if len(segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(segments))
	}
	s := segments[0]
	if s.DocumentID != "doc1" || s.StartChunk != 3 || s.EndChunk != 4 {
		t.Errorf("segment = %+v, want doc1 [3,4)", s)
	}
}

func TestExtractSegments_MergesAdjacentChunks(t *testing.T) {
	chunks := []RankedChunk{
		{DocumentID: "doc1", ChunkIndex: 3, Rank: 0, Score: 0.9},
		{DocumentID: "doc1", ChunkIndex: 4, Rank: 1, Score: 0.85},
		{DocumentID: "doc1", ChunkIndex: 5, Rank: 2, Score: 0.8},
	}
	params := DefaultRSEParams()
	segments := ExtractSegments(chunks, 10, params)

	if len(segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(segments))
	}
	s := segments[0]
	if s.StartChunk != 3 || s.EndChunk != 6 {
		t.Errorf("segment = [%d,%d), want [3,6)", s.StartChunk, s.EndChunk)
	}
}

func TestExtractSegments_DoesNotCrossDocuments(t *testing.T) {
	chunks := []RankedChunk{
		{DocumentID: "doc1", ChunkIndex: 5, Rank: 0, Score: 0.9},
		{DocumentID: "doc2", ChunkIndex: 0, Rank: 1, Score: 0.85},
	}
	params := DefaultRSEParams()
	segments := ExtractSegments(chunks, 10, params)

	if len(segments) != 2 {
		t.Fatalf("got %d segments, want 2", len(segments))
	}
	if segments[0].DocumentID == segments[1].DocumentID {
		t.Error("segments should be from different documents")
	}
}

func TestExtractSegments_RespectsMaxLength(t *testing.T) {
	chunks := []RankedChunk{
		{DocumentID: "doc1", ChunkIndex: 3, Rank: 0, Score: 0.9},
		{DocumentID: "doc1", ChunkIndex: 4, Rank: 1, Score: 0.85},
		{DocumentID: "doc1", ChunkIndex: 5, Rank: 2, Score: 0.8},
	}
	params := DefaultRSEParams()
	params.MaxLength = 2
	segments := ExtractSegments(chunks, 10, params)

	for _, s := range segments {
		length := s.EndChunk - s.StartChunk
		if length > 2 {
			t.Errorf("segment length %d exceeds max 2", length)
		}
	}
}

func TestExtractSegments_RespectsOverallMaxLength(t *testing.T) {
	chunks := []RankedChunk{
		{DocumentID: "doc1", ChunkIndex: 0, Rank: 0, Score: 0.95},
		{DocumentID: "doc1", ChunkIndex: 1, Rank: 1, Score: 0.9},
		{DocumentID: "doc1", ChunkIndex: 2, Rank: 2, Score: 0.85},
		{DocumentID: "doc2", ChunkIndex: 0, Rank: 3, Score: 0.8},
		{DocumentID: "doc2", ChunkIndex: 1, Rank: 4, Score: 0.75},
	}
	params := DefaultRSEParams()
	params.OverallMaxLength = 3
	segments := ExtractSegments(chunks, 10, params)

	total := 0
	for _, s := range segments {
		total += s.EndChunk - s.StartChunk
	}
	if total > 3 {
		t.Errorf("total segment length %d exceeds overall max 3", total)
	}
}

func TestExtractSegments_NoOverlap(t *testing.T) {
	chunks := []RankedChunk{
		{DocumentID: "doc1", ChunkIndex: 0, Rank: 0, Score: 0.95},
		{DocumentID: "doc1", ChunkIndex: 1, Rank: 1, Score: 0.9},
		{DocumentID: "doc1", ChunkIndex: 2, Rank: 2, Score: 0.85},
		{DocumentID: "doc1", ChunkIndex: 3, Rank: 3, Score: 0.8},
		{DocumentID: "doc1", ChunkIndex: 4, Rank: 4, Score: 0.75},
	}
	params := DefaultRSEParams()
	params.MaxLength = 2
	segments := ExtractSegments(chunks, 10, params)

	for i := 0; i < len(segments); i++ {
		for j := i + 1; j < len(segments); j++ {
			if segments[i].DocumentID != segments[j].DocumentID {
				continue
			}
			if segments[i].StartChunk < segments[j].EndChunk && segments[j].StartChunk < segments[i].EndChunk {
				t.Errorf("segments overlap: [%d,%d) and [%d,%d)",
					segments[i].StartChunk, segments[i].EndChunk,
					segments[j].StartChunk, segments[j].EndChunk)
			}
		}
	}
}

func TestExtractSegments_Empty(t *testing.T) {
	segments := ExtractSegments(nil, 10, DefaultRSEParams())
	if len(segments) != 0 {
		t.Errorf("got %d segments for nil input, want 0", len(segments))
	}
}

func TestExtractSegments_LowScoresFiltered(t *testing.T) {
	chunks := []RankedChunk{
		{DocumentID: "doc1", ChunkIndex: 0, Rank: 0, Score: 0.01},
	}
	params := DefaultRSEParams()
	params.MinimumValue = 0.5
	segments := ExtractSegments(chunks, 10, params)

	if len(segments) != 0 {
		t.Errorf("got %d segments, want 0 (score too low)", len(segments))
	}
}

func TestRelevanceValue(t *testing.T) {
	score := 0.8
	rank := 0
	decayRate := 30.0
	penalty := 0.18

	got := relevanceValue(rank, score, decayRate, penalty)
	want := math.Exp(float64(-rank)/decayRate)*score - penalty
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("relevanceValue(%d, %.1f) = %f, want %f", rank, score, got, want)
	}
}

func TestExtractSegments_SegmentsSortedByScore(t *testing.T) {
	chunks := []RankedChunk{
		{DocumentID: "doc1", ChunkIndex: 0, Rank: 2, Score: 0.7},
		{DocumentID: "doc2", ChunkIndex: 0, Rank: 0, Score: 0.95},
		{DocumentID: "doc3", ChunkIndex: 0, Rank: 1, Score: 0.8},
	}
	params := DefaultRSEParams()
	segments := ExtractSegments(chunks, 10, params)

	for i := 1; i < len(segments); i++ {
		if segments[i].Score > segments[i-1].Score {
			t.Errorf("segments not sorted: score[%d]=%f > score[%d]=%f",
				i, segments[i].Score, i-1, segments[i-1].Score)
		}
	}
}
