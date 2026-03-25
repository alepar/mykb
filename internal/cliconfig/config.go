package cliconfig

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds CLI client configuration.
type Config struct {
	Host        string `toml:"host"`
	Lines       int    `toml:"lines"`
	TopK        int    `toml:"top_k"`
	VectorDepth int    `toml:"vector_depth"`
	FTSDepth    int    `toml:"fts_depth"`
	RerankDepth int    `toml:"rerank_depth"`
}

// DefaultConfigPath returns ~/.mykb.conf.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mykb.conf")
}

// Load reads config from the given path, applying defaults for any missing fields.
// If path is empty, it tries DefaultConfigPath(). Missing files are silently ignored.
func Load(path string) Config {
	cfg := Config{
		Host:        "http://localhost:9091",
		Lines:       5,
		TopK:        10,
		VectorDepth: 1000,
		FTSDepth:    1000,
		RerankDepth: 1000,
	}

	if path == "" {
		path = DefaultConfigPath()
	}

	if path != "" {
		_, _ = toml.DecodeFile(path, &cfg)
	}

	return cfg
}
