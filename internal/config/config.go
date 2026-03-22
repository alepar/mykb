package config

import "os"

type Config struct {
	PostgresDSN     string
	QdrantGRPCHost  string
	MeilisearchHost string
	MeilisearchKey  string
	Crawl4AIURL     string
	VoyageAPIKey string
	GRPCPort     string
	HTTPPort     string
	DataDir      string

	// Model settings
	VoyageEmbedModel     string
	VoyageEmbedDimension int
	VoyageRerankModel    string

	// Pipeline settings
	ChunkTargetTokens int
	ChunkMaxTokens    int
	EmbedBatchSize    int
	MaxRetries        int

	// Search defaults
	DefaultTopK        int
	DefaultVectorDepth int
	DefaultFTSDepth    int
	DefaultRerankDepth int
	RRFConstant        int

	// RSE defaults
	RSEMaxLength              int
	RSEOverallMaxLength       int
	RSEMinimumValue           float64
	RSEIrrelevantChunkPenalty float64
	RSEDecayRate              float64
}

func Load() *Config {
	return &Config{
		PostgresDSN:     envOr("POSTGRES_DSN", "postgres://mykb:mykb@localhost:5432/mykb?sslmode=disable"),
		QdrantGRPCHost:  envOr("QDRANT_GRPC_HOST", "localhost:6334"),
		MeilisearchHost: envOr("MEILISEARCH_HOST", "http://localhost:7700"),
		MeilisearchKey:  os.Getenv("MEILISEARCH_KEY"),
		Crawl4AIURL:     envOr("CRAWL4AI_URL", "http://localhost:11235"),
		VoyageAPIKey: os.Getenv("VOYAGE_API_KEY"),
		GRPCPort:     envOr("GRPC_PORT", "9090"),
		HTTPPort:     envOr("HTTP_PORT", "9091"),
		DataDir:      envOr("DATA_DIR", "data/documents"),

		VoyageEmbedModel:     "voyage-context-3",
		VoyageEmbedDimension: 2048,
		VoyageRerankModel:    "rerank-2",

		ChunkTargetTokens: 800,
		ChunkMaxTokens:    1500,
		EmbedBatchSize:    128,
		MaxRetries:        5,

		DefaultTopK:        10,
		DefaultVectorDepth: 100,
		DefaultFTSDepth:    100,
		DefaultRerankDepth: 50,
		RRFConstant:        60,

		RSEMaxLength:              15,
		RSEOverallMaxLength:       30,
		RSEMinimumValue:           0.5,
		RSEIrrelevantChunkPenalty: 0.18,
		RSEDecayRate:              30,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
