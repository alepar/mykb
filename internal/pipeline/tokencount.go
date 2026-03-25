package pipeline

import (
	"log"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// voyageTokenMultiplier accounts for the difference between cl100k_base
// (OpenAI GPT-4) and Qwen2 (Voyage AI) tokenizers. Voyage's tokenizer
// produces ~1.1-1.2x more tokens than cl100k_base per Voyage docs.
const voyageTokenMultiplier = 1.2

var (
	bpeOnce sync.Once
	bpeCodec tokenizer.Codec
)

func getBPE() tokenizer.Codec {
	bpeOnce.Do(func() {
		var err error
		bpeCodec, err = tokenizer.Get(tokenizer.Cl100kBase)
		if err != nil {
			log.Printf("tokenizer: failed to load cl100k_base, falling back to len/4: %v", err)
		}
	})
	return bpeCodec
}

// countTokens returns an estimated token count for a string using the
// cl100k_base BPE tokenizer with a 1.2x multiplier to approximate
// Voyage AI's Qwen2 tokenizer. Falls back to len/4 if the tokenizer
// fails to load.
func countTokens(s string) int {
	codec := getBPE()
	if codec == nil {
		return len(s) / 4
	}
	ids, _, _ := codec.Encode(s)
	return int(float64(len(ids)) * voyageTokenMultiplier)
}
