package cliconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load("")
	if cfg.Host != "localhost:9090" {
		t.Errorf("Host = %q, want %q", cfg.Host, "localhost:9090")
	}
	if cfg.Lines != 5 {
		t.Errorf("Lines = %d, want 5", cfg.Lines)
	}
	if cfg.TopK != 10 {
		t.Errorf("TopK = %d, want 10", cfg.TopK)
	}
	if cfg.VectorDepth != 1000 {
		t.Errorf("VectorDepth = %d, want 1000", cfg.VectorDepth)
	}
	if cfg.FTSDepth != 1000 {
		t.Errorf("FTSDepth = %d, want 1000", cfg.FTSDepth)
	}
	if cfg.RerankDepth != 1000 {
		t.Errorf("RerankDepth = %d, want 1000", cfg.RerankDepth)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "mykb.conf")
	err := os.WriteFile(confPath, []byte(`
host = "myserver:9090"
lines = 3
top_k = 20
vector_depth = 500
fts_depth = 500
rerank_depth = 200
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg := Load(confPath)
	if cfg.Host != "myserver:9090" {
		t.Errorf("Host = %q, want %q", cfg.Host, "myserver:9090")
	}
	if cfg.Lines != 3 {
		t.Errorf("Lines = %d, want 3", cfg.Lines)
	}
	if cfg.TopK != 20 {
		t.Errorf("TopK = %d, want 20", cfg.TopK)
	}
	if cfg.VectorDepth != 500 {
		t.Errorf("VectorDepth = %d, want 500", cfg.VectorDepth)
	}
}

func TestPartialOverride(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "mykb.conf")
	err := os.WriteFile(confPath, []byte(`host = "custom:9090"`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg := Load(confPath)
	if cfg.Host != "custom:9090" {
		t.Errorf("Host = %q, want %q", cfg.Host, "custom:9090")
	}
	if cfg.Lines != 5 {
		t.Errorf("Lines = %d, want 5", cfg.Lines)
	}
	if cfg.TopK != 10 {
		t.Errorf("TopK = %d, want 10", cfg.TopK)
	}
}

func TestMissingFileUsesDefaults(t *testing.T) {
	cfg := Load("/nonexistent/path/mykb.conf")
	if cfg.Host != "localhost:9090" {
		t.Errorf("Host = %q, want %q", cfg.Host, "localhost:9090")
	}
}
