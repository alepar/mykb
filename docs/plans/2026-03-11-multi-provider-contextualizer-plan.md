# Multi-Provider Contextualizer Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add OpenAI-compatible provider support (OpenCode Zen / MiniMax M2.5) alongside existing Anthropic provider for chunk contextualization, with token usage logging on both.

**Architecture:** Extract a `ContextualizeProvider` interface from the existing `Contextualizer`. Refactor the Anthropic implementation to implement it, then add a new OpenAI-compatible implementation using `github.com/sashabaranov/go-openai`. The worker and main wiring switch between them via config.

**Tech Stack:** Go, `github.com/sashabaranov/go-openai`, `github.com/anthropics/anthropic-sdk-go`

---

### Task 1: Extract ContextualizeProvider interface and refactor Anthropic implementation

**Files:**
- Modify: `internal/pipeline/contextualize.go`
- Modify: `internal/pipeline/contextualize_test.go`
- Modify: `internal/worker/worker.go`

The current `Contextualizer` struct is used directly by the worker. We need to extract an interface so the worker can accept either provider.

**Step 1: Define the interface and rename the Anthropic struct**

In `internal/pipeline/contextualize.go`, add this interface at the top of the file (after imports), and rename `Contextualizer` to `AnthropicContextualizer`:

```go
// ContextualizeProvider generates short context descriptions for chunks
// relative to their source document.
type ContextualizeProvider interface {
	Contextualize(ctx context.Context, document, chunk string) (string, error)
}
```

Rename `Contextualizer` → `AnthropicContextualizer` and `NewContextualizer` → `NewAnthropicContextualizer` throughout the file. The `Contextualize` method signature already matches the interface, so no change needed there.

**Step 2: Add token usage logging to Anthropic implementation**

In `AnthropicContextualizer.Contextualize()`, after the successful API call (after `resp, err := c.client.Messages.New(...)`), add logging:

```go
log.Printf("contextualize [anthropic]: input=%d output=%d cache_creation=%d cache_read=%d",
    resp.Usage.InputTokens, resp.Usage.OutputTokens,
    resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens)
```

Add `"log"` to the imports.

**Step 3: Update worker to use the interface**

In `internal/worker/worker.go`, change the `contextualizer` field type from `*pipeline.Contextualizer` to `pipeline.ContextualizeProvider`. Update the `NewWorker` constructor parameter type to match:

```go
// In Worker struct:
contextualizer pipeline.ContextualizeProvider

// In NewWorker signature:
func NewWorker(
    pg *storage.PostgresStore,
    fs *storage.FilesystemStore,
    crawler *pipeline.Crawler,
    contextualizer pipeline.ContextualizeProvider,
    embedder *pipeline.Embedder,
    indexer *pipeline.Indexer,
    cfg *config.Config,
) *Worker {
```

No other changes needed in worker.go — it already calls `w.contextualizer.Contextualize(...)` which matches the interface.

**Step 4: Update tests**

In `internal/pipeline/contextualize_test.go`:
- Rename `NewContextualizer` → `NewAnthropicContextualizer` in `TestNewContextualizer` and `TestContextualizeIntegration`
- Update `TestNewContextualizer` to check field `c.model` still works (the struct is now `AnthropicContextualizer`, but the test accesses the unexported field — this works because it's in the same package)

**Step 5: Run tests to verify refactor**

Run: `go build ./... 2>&1 | grep -v "permission denied"` and `go test ./internal/pipeline/ -v -run "TestBuild|TestNew|TestContextualize" -count=1`

Expected: All compile, all unit tests pass.

**Step 6: Commit**

```bash
git add internal/pipeline/contextualize.go internal/pipeline/contextualize_test.go internal/worker/worker.go
git commit -m "refactor: extract ContextualizeProvider interface from Anthropic implementation"
```

---

### Task 2: Add OpenAI-compatible contextualizer implementation

**Files:**
- Create: `internal/pipeline/contextualize_openai.go`
- Create: `internal/pipeline/contextualize_openai_test.go`

**Step 1: Install the go-openai dependency**

Run: `go get github.com/sashabaranov/go-openai`

**Step 2: Write the failing test**

Create `internal/pipeline/contextualize_openai_test.go`:

```go
package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestOpenAIContextualizer_Contextualize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Verify request structure.
		var req openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Messages) != 2 {
			t.Errorf("expected 2 messages (system + user), got %d", len(req.Messages))
		}
		if req.Messages[0].Role != "system" {
			t.Errorf("expected first message role 'system', got %q", req.Messages[0].Role)
		}
		if req.Messages[1].Role != "user" {
			t.Errorf("expected second message role 'user', got %q", req.Messages[1].Role)
		}
		if req.MaxTokens != 256 {
			t.Errorf("expected max_tokens 256, got %d", req.MaxTokens)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{{
				Message: openai.ChatCompletionMessage{
					Role:    "assistant",
					Content: "This chunk describes Go concurrency primitives.",
				},
			}},
			Usage: openai.Usage{
				PromptTokens:     100,
				CompletionTokens: 15,
				PromptTokensDetails: &openai.PromptTokensDetails{
					CachedTokens: 80,
				},
			},
		})
	}))
	defer server.Close()

	c := NewOpenAIContextualizer(server.URL, "test-key", "test-model")
	result, err := c.Contextualize(context.Background(), "full document text", "chunk text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "This chunk describes Go concurrency primitives." {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestOpenAIContextualizer_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := NewOpenAIContextualizer(server.URL, "test-key", "test-model")
	_, err := c.Contextualize(context.Background(), "doc", "chunk")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestOpenAIContextualizer_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{},
		})
	}))
	defer server.Close()

	c := NewOpenAIContextualizer(server.URL, "test-key", "test-model")
	_, err := c.Contextualize(context.Background(), "doc", "chunk")
	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
}

func TestNewOpenAIContextualizer(t *testing.T) {
	c := NewOpenAIContextualizer("https://opencode.ai/zen/v1", "test-key", "minimax-m2.5")
	if c == nil {
		t.Fatal("NewOpenAIContextualizer returned nil")
	}
	if c.model != "minimax-m2.5" {
		t.Errorf("model = %q, want %q", c.model, "minimax-m2.5")
	}
}
```

**Step 3: Run test to verify it fails**

Run: `go test ./internal/pipeline/ -v -run TestOpenAI -count=1`

Expected: FAIL — `NewOpenAIContextualizer` not defined.

**Step 4: Write the implementation**

Create `internal/pipeline/contextualize_openai.go`:

```go
package pipeline

import (
	"context"
	"fmt"
	"log"

	openai "github.com/sashabaranov/go-openai"
)

// OpenAIContextualizer uses an OpenAI-compatible API to generate short context
// descriptions for chunks relative to their source document.
// Prompt caching is automatic and server-side (no explicit cache_control needed).
type OpenAIContextualizer struct {
	client *openai.Client
	model  string
}

// NewOpenAIContextualizer creates a contextualizer that calls an OpenAI-compatible
// API endpoint (e.g. OpenCode Zen).
func NewOpenAIContextualizer(baseURL, apiKey, model string) *OpenAIContextualizer {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	client := openai.NewClientWithConfig(cfg)
	return &OpenAIContextualizer{
		client: client,
		model:  model,
	}
}

// Contextualize calls the OpenAI-compatible chat completions API to generate
// a short context description that situates the given chunk within the full document.
func (c *OpenAIContextualizer) Contextualize(ctx context.Context, document, chunk string) (string, error) {
	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       c.model,
		MaxTokens:   256,
		Temperature: 0.0,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleSystem,
				Content: "<document>\n" + document + "\n</document>",
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: buildChunkPrompt(chunk),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("contextualize API call: %w", err)
	}

	cachedTokens := 0
	if resp.Usage.PromptTokensDetails != nil {
		cachedTokens = resp.Usage.PromptTokensDetails.CachedTokens
	}
	log.Printf("contextualize [openai]: prompt=%d completion=%d cached=%d",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, cachedTokens)

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("contextualize: empty response from API")
	}

	return resp.Choices[0].Message.Content, nil
}
```

Note: The document goes in a system message (for prefix caching) and the chunk prompt goes in the user message. The `buildChunkPrompt()` function is already defined in `contextualize.go` and shared by both implementations.

**Step 5: Run tests to verify they pass**

Run: `go test ./internal/pipeline/ -v -run TestOpenAI -count=1`

Expected: All 4 tests PASS.

**Step 6: Commit**

```bash
git add internal/pipeline/contextualize_openai.go internal/pipeline/contextualize_openai_test.go go.mod go.sum
git commit -m "feat: add OpenAI-compatible contextualizer for OpenCode Zen"
```

---

### Task 3: Add config and wiring

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/mykb/main.go`
- Modify: `.env`

**Step 1: Add new config fields**

In `internal/config/config.go`, add these fields to the `Config` struct:

```go
// Provider selection
ContextualizeProvider string

// OpenAI-compatible settings
OpenAICompatAPIKey  string
OpenAICompatBaseURL string
OpenAICompatModel   string
```

In the `Load()` function, add:

```go
ContextualizeProvider: envOr("CONTEXTUALIZE_PROVIDER", "anthropic"),

OpenAICompatAPIKey:  os.Getenv("OPENAI_COMPAT_API_KEY"),
OpenAICompatBaseURL: envOr("OPENAI_COMPAT_BASE_URL", "https://opencode.ai/zen/v1"),
OpenAICompatModel:   envOr("OPENAI_COMPAT_MODEL", "minimax-m2.5"),
```

**Step 2: Update main.go wiring**

In `cmd/mykb/main.go`, replace:

```go
contextualizer := pipeline.NewContextualizer(cfg.AnthropicAPIKey, cfg.ClaudeModel)
```

With:

```go
var contextualizer pipeline.ContextualizeProvider
switch cfg.ContextualizeProvider {
case "openai":
    contextualizer = pipeline.NewOpenAIContextualizer(cfg.OpenAICompatBaseURL, cfg.OpenAICompatAPIKey, cfg.OpenAICompatModel)
    log.Printf("using OpenAI-compatible contextualizer: %s @ %s", cfg.OpenAICompatModel, cfg.OpenAICompatBaseURL)
default:
    contextualizer = pipeline.NewAnthropicContextualizer(cfg.AnthropicAPIKey, cfg.ClaudeModel)
    log.Printf("using Anthropic contextualizer: %s", cfg.ClaudeModel)
}
```

**Step 3: Update .env**

Add the new keys to `.env`:

```
CONTEXTUALIZE_PROVIDER=openai
OPENAI_COMPAT_API_KEY=sk-QLkGzZJpX2K773ignVZr2CHvM1m1NsC31HSKA23qfGmlAhn2syzaKPgHycXw3C6D
```

Note: `OPENAI_COMPAT_BASE_URL` and `OPENAI_COMPAT_MODEL` use defaults, so no need to set them unless overriding.

**Step 4: Build and verify**

Run: `go build ./cmd/... ./internal/... ./gen/...`

Expected: Clean build, no errors.

**Step 5: Commit**

```bash
git add internal/config/config.go cmd/mykb/main.go
git commit -m "feat: wire multi-provider contextualizer with config switching"
```

Do NOT commit `.env` (contains secrets).

---

### Task 4: Integration test with OpenCode Zen

**Step 1: Start infrastructure**

Ensure docker compose services are running:

```bash
docker compose up -d postgres qdrant meilisearch crawl4ai
```

**Step 2: Clean previous test data**

```bash
docker compose exec postgres psql -U mykb -d mykb -c "DELETE FROM chunks; DELETE FROM documents;"
rm -rf /tmp/mykb-data/documents/*
```

**Step 3: Start the service with OpenAI provider**

```bash
export $(grep -v '^#' .env | xargs)
export POSTGRES_DSN="postgres://mykb:mykb@localhost:5433/mykb?sslmode=disable"
export QDRANT_GRPC_HOST="localhost:6335"
export MEILISEARCH_HOST="http://localhost:7701"
export CRAWL4AI_URL="http://localhost:11235"
export DATA_DIR="/tmp/mykb-data/documents"
go run ./cmd/mykb/
```

Expected log output should include: `using OpenAI-compatible contextualizer: minimax-m2.5 @ https://opencode.ai/zen/v1`

**Step 4: Test IngestURL**

```bash
grpcurl -plaintext -d '{"url": "https://go.dev/blog/defer-panic-and-recover"}' localhost:9090 mykb.v1.KBService/IngestURL
```

Expected: Streaming progress through all stages (CRAWLING → CHUNKING → CONTEXTUALIZING → EMBEDDING → INDEXING → DONE). Server logs should show `contextualize [openai]: prompt=N completion=N cached=N` lines, with `cached` values increasing after the first chunk.

**Step 5: Test Query**

```bash
grpcurl -plaintext -d '{"query": "how does defer work in Go?"}' localhost:9090 mykb.v1.KBService/Query
```

Expected: Ranked results returned.
