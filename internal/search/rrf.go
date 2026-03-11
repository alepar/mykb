package search

import "sort"

type ScoredID struct {
	ID    string
	Score float64
}

func RRF(k int, topK int, lists ...[]ScoredID) []ScoredID {
	scores := make(map[string]float64)
	for _, list := range lists {
		for rank, item := range list {
			scores[item.ID] += 1.0 / float64(k+rank+1)
		}
	}
	results := make([]ScoredID, 0, len(scores))
	for id, score := range scores {
		results = append(results, ScoredID{ID: id, Score: score})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	return results
}
