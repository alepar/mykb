package pipeline

import (
	"context"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// Contextualizer uses the Anthropic Claude API to generate short context
// descriptions for chunks relative to their source document. This enables
// contextual retrieval — each chunk is annotated with situational context
// that improves search relevance.
//
// When processing multiple chunks from the same document, prompt caching
// (via cache_control: ephemeral) means the document content is cached after
// the first call, and subsequent chunks reuse the cache at ~90% discount.
type Contextualizer struct {
	client anthropic.Client
	model  string
}

// NewContextualizer creates a Contextualizer that calls the Anthropic API.
func NewContextualizer(apiKey, model string) *Contextualizer {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Contextualizer{
		client: client,
		model:  model,
	}
}

// BuildDocumentBlock returns a text content block containing the full document
// wrapped in <document> tags, with cache_control set to ephemeral for prompt caching.
func BuildDocumentBlock(document string) anthropic.ContentBlockParamUnion {
	return anthropic.ContentBlockParamUnion{
		OfText: &anthropic.TextBlockParam{
			Text:         "<document>\n" + document + "\n</document>",
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		},
	}
}

// BuildChunkBlock returns a text content block containing the chunk prompt.
func BuildChunkBlock(chunk string) anthropic.ContentBlockParamUnion {
	return anthropic.NewTextBlock(buildChunkPrompt(chunk))
}

func buildChunkPrompt(chunk string) string {
	return fmt.Sprintf(
		"Here is the chunk we want to situate within the whole document\n<chunk>\n%s\n</chunk>\n\nPlease give a short succinct context to situate this chunk within the overall document for the purposes of improving search retrieval of the chunk.\nAnswer only with the succinct context and nothing else.",
		chunk,
	)
}

// Contextualize calls the Claude API to generate a short context description
// that situates the given chunk within the full document.
func (c *Contextualizer) Contextualize(ctx context.Context, document string, chunk string) (string, error) {
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:    anthropic.Model(c.model),
		MaxTokens: 256,
		Temperature: param.NewOpt(0.0),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				BuildDocumentBlock(document),
				BuildChunkBlock(chunk),
			),
		},
	})
	if err != nil {
		return "", fmt.Errorf("contextualize API call: %w", err)
	}

	if len(resp.Content) == 0 {
		return "", fmt.Errorf("contextualize: empty response from API")
	}

	// The first content block should be text.
	block := resp.Content[0]
	if block.Type != "text" {
		return "", fmt.Errorf("contextualize: unexpected content block type %q", block.Type)
	}
	return block.Text, nil
}
