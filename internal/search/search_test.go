package search

import (
	"testing"

	"mykb/internal/config"
)

func TestApplyDefaults_AllZero(t *testing.T) {
	cfg := &config.Config{
		DefaultTopK:        10,
		DefaultVectorDepth: 100,
		DefaultFTSDepth:    100,
		DefaultRerankDepth: 50,
		RRFConstant:        60,
	}

	p := applyDefaults(SearchParams{Query: "test"}, cfg)

	if p.TopK != 10 {
		t.Errorf("TopK = %d, want 10", p.TopK)
	}
	if p.VectorDepth != 100 {
		t.Errorf("VectorDepth = %d, want 100", p.VectorDepth)
	}
	if p.FTSDepth != 100 {
		t.Errorf("FTSDepth = %d, want 100", p.FTSDepth)
	}
	if p.RerankDepth != 50 {
		t.Errorf("RerankDepth = %d, want 50", p.RerankDepth)
	}
	if p.Query != "test" {
		t.Errorf("Query = %q, want %q", p.Query, "test")
	}
}

func TestApplyDefaults_NoOverride(t *testing.T) {
	cfg := &config.Config{
		DefaultTopK:        10,
		DefaultVectorDepth: 100,
		DefaultFTSDepth:    100,
		DefaultRerankDepth: 50,
	}

	p := applyDefaults(SearchParams{
		Query:       "test",
		TopK:        5,
		VectorDepth: 200,
		FTSDepth:    150,
		RerankDepth: 25,
	}, cfg)

	if p.TopK != 5 {
		t.Errorf("TopK = %d, want 5", p.TopK)
	}
	if p.VectorDepth != 200 {
		t.Errorf("VectorDepth = %d, want 200", p.VectorDepth)
	}
	if p.FTSDepth != 150 {
		t.Errorf("FTSDepth = %d, want 150", p.FTSDepth)
	}
	if p.RerankDepth != 25 {
		t.Errorf("RerankDepth = %d, want 25", p.RerankDepth)
	}
}

func TestApplyDefaults_Partial(t *testing.T) {
	cfg := &config.Config{
		DefaultTopK:        10,
		DefaultVectorDepth: 100,
		DefaultFTSDepth:    100,
		DefaultRerankDepth: 50,
	}

	p := applyDefaults(SearchParams{
		Query: "test",
		TopK:  7,
		// VectorDepth, FTSDepth, RerankDepth are zero → should get defaults
	}, cfg)

	if p.TopK != 7 {
		t.Errorf("TopK = %d, want 7", p.TopK)
	}
	if p.VectorDepth != 100 {
		t.Errorf("VectorDepth = %d, want 100", p.VectorDepth)
	}
	if p.FTSDepth != 100 {
		t.Errorf("FTSDepth = %d, want 100", p.FTSDepth)
	}
	if p.RerankDepth != 50 {
		t.Errorf("RerankDepth = %d, want 50", p.RerankDepth)
	}
}

func TestNewHybridSearcher(t *testing.T) {
	cfg := &config.Config{}
	h := NewHybridSearcher(nil, nil, nil, nil, nil, cfg)
	if h == nil {
		t.Fatal("NewHybridSearcher returned nil")
	}
	if h.cfg != cfg {
		t.Error("cfg not set correctly")
	}
}
