package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

func TestBuildDocumentBlock(t *testing.T) {
	doc := "This is the full document content."
	block := BuildDocumentBlock(doc)

	if block.OfText == nil {
		t.Fatal("expected OfText to be non-nil")
	}

	want := "<document>\n" + doc + "\n</document>"
	if block.OfText.Text != want {
		t.Errorf("document block text = %q, want %q", block.OfText.Text, want)
	}

	// Verify cache_control is set (ephemeral type).
	if block.OfText.CacheControl.Type != "ephemeral" {
		t.Errorf("cache_control.type = %q, want %q", block.OfText.CacheControl.Type, "ephemeral")
	}
}

func TestBuildChunkBlock(t *testing.T) {
	chunk := "Some chunk content here."
	block := BuildChunkBlock(chunk)

	if block.OfText == nil {
		t.Fatal("expected OfText to be non-nil")
	}

	text := block.OfText.Text
	if !strings.Contains(text, "<chunk>") {
		t.Error("chunk block should contain <chunk> tag")
	}
	if !strings.Contains(text, "</chunk>") {
		t.Error("chunk block should contain </chunk> tag")
	}
	if !strings.Contains(text, chunk) {
		t.Errorf("chunk block should contain the chunk content %q", chunk)
	}
	if !strings.Contains(text, "situate this chunk within the overall document") {
		t.Error("chunk block should contain the situating prompt")
	}
	if !strings.Contains(text, "Answer only with the succinct context") {
		t.Error("chunk block should instruct to answer only with context")
	}
}

func TestBuildChunkPrompt(t *testing.T) {
	chunk := "Example chunk."
	prompt := buildChunkPrompt(chunk)

	// Verify exact structure.
	if !strings.HasPrefix(prompt, "Here is the chunk we want to situate within the whole document\n<chunk>\n") {
		t.Error("prompt should start with the expected preamble and <chunk> tag")
	}
	if !strings.Contains(prompt, "Example chunk.") {
		t.Error("prompt should contain the chunk content")
	}
	if !strings.HasSuffix(prompt, "Answer only with the succinct context and nothing else.") {
		t.Error("prompt should end with the expected instruction")
	}
}

func TestNewContextualizer(t *testing.T) {
	// Compile test: verify the constructor returns a non-nil value.
	c := NewContextualizer("test-key", "claude-haiku-4-5-20251001")
	if c == nil {
		t.Fatal("NewContextualizer returned nil")
	}
	if c.model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want %q", c.model, "claude-haiku-4-5-20251001")
	}
}

func TestContextualizeIntegration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}

	c := NewContextualizer(apiKey, "claude-haiku-4-5-20251001")

	document := `# Go Programming Guide

## Introduction
Go is a statically typed, compiled language designed at Google.

## Concurrency
Go provides goroutines and channels for concurrent programming.
Goroutines are lightweight threads managed by the Go runtime.

## Error Handling
Go uses explicit error returns instead of exceptions.
Functions return an error value as the last return value.`

	chunk := `Go provides goroutines and channels for concurrent programming.
Goroutines are lightweight threads managed by the Go runtime.`

	ctx := context.Background()
	result, err := c.Contextualize(ctx, document, chunk)
	if err != nil {
		t.Fatalf("Contextualize() error: %v", err)
	}
	if result == "" {
		t.Error("Contextualize() returned empty string")
	}
	t.Logf("Context result: %s", result)
}

// TestContextualizeCompile verifies that all types used in Contextualize
// are correctly referenced and the code compiles.
func TestContextualizeCompile(t *testing.T) {
	// Verify that the types we use from the SDK are accessible.
	_ = anthropic.MessageNewParams{}
	_ = anthropic.ContentBlockParamUnion{}
	_ = anthropic.TextBlockParam{}
	_ = anthropic.NewCacheControlEphemeralParam()
	_ = anthropic.NewTextBlock("test")
	_ = anthropic.NewUserMessage()
}
