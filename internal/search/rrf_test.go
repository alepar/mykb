package search

import (
	"testing"
)

func TestRRF_SingleList(t *testing.T) {
	list := []ScoredID{
		{ID: "a", Score: 10},
		{ID: "b", Score: 5},
		{ID: "c", Score: 1},
	}
	result := RRF(60, 0, list)
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
	// rank 0 -> 1/(60+0+1)=1/61, rank 1 -> 1/62, rank 2 -> 1/63
	expected := []struct {
		id    string
		score float64
	}{
		{"a", 1.0 / 61},
		{"b", 1.0 / 62},
		{"c", 1.0 / 63},
	}
	for i, exp := range expected {
		if result[i].ID != exp.id {
			t.Errorf("result[%d].ID = %q, want %q", i, result[i].ID, exp.id)
		}
		if !closeEnough(result[i].Score, exp.score) {
			t.Errorf("result[%d].Score = %f, want %f", i, result[i].Score, exp.score)
		}
	}
}

func TestRRF_TwoOverlappingLists(t *testing.T) {
	list1 := []ScoredID{
		{ID: "a", Score: 10},
		{ID: "b", Score: 5},
	}
	list2 := []ScoredID{
		{ID: "b", Score: 10},
		{ID: "a", Score: 5},
	}
	result := RRF(60, 0, list1, list2)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	// "a": rank0 in list1 (1/61) + rank1 in list2 (1/62) = 1/61 + 1/62
	// "b": rank1 in list1 (1/62) + rank0 in list2 (1/61) = 1/62 + 1/61
	// Both should have the same score
	expectedScore := 1.0/61 + 1.0/62
	for _, r := range result {
		if !closeEnough(r.Score, expectedScore) {
			t.Errorf("ID %q: score = %f, want %f", r.ID, r.Score, expectedScore)
		}
	}
}

func TestRRF_TwoDisjointLists(t *testing.T) {
	list1 := []ScoredID{
		{ID: "a", Score: 10},
		{ID: "b", Score: 5},
	}
	list2 := []ScoredID{
		{ID: "c", Score: 10},
		{ID: "d", Score: 5},
	}
	result := RRF(60, 0, list1, list2)
	if len(result) != 4 {
		t.Fatalf("expected 4 results, got %d", len(result))
	}
	ids := make(map[string]bool)
	for _, r := range result {
		ids[r.ID] = true
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if !ids[id] {
			t.Errorf("missing ID %q in results", id)
		}
	}
}

func TestRRF_TopKLimitsOutput(t *testing.T) {
	list := []ScoredID{
		{ID: "a", Score: 10},
		{ID: "b", Score: 5},
		{ID: "c", Score: 1},
	}
	result := RRF(60, 2, list)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	// Should be the top 2 by RRF score: "a" (rank 0) and "b" (rank 1)
	if result[0].ID != "a" {
		t.Errorf("result[0].ID = %q, want %q", result[0].ID, "a")
	}
	if result[1].ID != "b" {
		t.Errorf("result[1].ID = %q, want %q", result[1].ID, "b")
	}
}

func TestRRF_TopKZeroMeansNoLimit(t *testing.T) {
	list := []ScoredID{
		{ID: "a", Score: 10},
		{ID: "b", Score: 5},
		{ID: "c", Score: 1},
	}
	result := RRF(60, 0, list)
	if len(result) != 3 {
		t.Fatalf("expected 3 results (no limit), got %d", len(result))
	}
}

func TestRRF_EmptyInput(t *testing.T) {
	result := RRF(60, 0)
	if len(result) != 0 {
		t.Fatalf("expected 0 results, got %d", len(result))
	}

	result = RRF(60, 0, []ScoredID{})
	if len(result) != 0 {
		t.Fatalf("expected 0 results for empty list, got %d", len(result))
	}
}

func TestRRF_DescendingOrder(t *testing.T) {
	list1 := []ScoredID{
		{ID: "a", Score: 10},
		{ID: "b", Score: 5},
		{ID: "c", Score: 1},
	}
	list2 := []ScoredID{
		{ID: "c", Score: 10},
		{ID: "b", Score: 5},
		{ID: "a", Score: 1},
	}
	result := RRF(60, 0, list1, list2)
	for i := 1; i < len(result); i++ {
		if result[i].Score > result[i-1].Score {
			t.Errorf("results not in descending order: result[%d].Score=%f > result[%d].Score=%f",
				i, result[i].Score, i-1, result[i-1].Score)
		}
	}
}

func closeEnough(a, b float64) bool {
	const eps = 1e-12
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < eps
}
