# Multi-Provider Contextualizer Design

**Goal:** Support both Anthropic and OpenAI-compatible providers (OpenCode Zen / MiniMax M2.5) for chunk contextualization, with token usage logging.

## Interface

```go
type ContextualizeProvider interface {
    Contextualize(ctx context.Context, document, chunk string) (string, error)
}
```

Both `AnthropicContextualizer` and `OpenAIContextualizer` implement this.

## OpenAI-compatible implementation

- Uses `github.com/sashabaranov/go-openai` with custom base URL (`https://opencode.ai/zen/v1`)
- Model: `minimax-m2.5` (configurable)
- Same prompt structure as Anthropic: document text first, chunk prompt second
- Temperature 0.0, max_tokens 256
- Automatic prompt caching (server-side, no explicit cache_control needed)
- Logs `PromptTokens`, `CompletionTokens`, `CachedTokens` via standard `log`

## Anthropic implementation

- Existing code refactored to implement the interface
- Keeps `cache_control: ephemeral` for prompt caching
- Adds token usage logging (input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens)

## Config changes

New env vars:
- `CONTEXTUALIZE_PROVIDER` — `"anthropic"` or `"openai"` (default: `"anthropic"`)
- `OPENAI_COMPAT_API_KEY` — key for OpenCode Zen
- `OPENAI_COMPAT_BASE_URL` — defaults to `https://opencode.ai/zen/v1`
- `OPENAI_COMPAT_MODEL` — defaults to `minimax-m2.5`

## Wiring

`cmd/mykb/main.go` checks `CONTEXTUALIZE_PROVIDER` and constructs the appropriate implementation. The worker/pipeline only sees the interface.

## What stays the same

- Pipeline, worker, all other code untouched
- Prompt text identical across both providers
