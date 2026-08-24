package builtin

import (
	"strings"

	"github.com/eaglc/codepilot/internal/llm"
)

// EnrichOpenAIModels applies published model limits that are not included in
// the OpenAI-compatible /models response. Unknown/custom IDs remain unset.
func EnrichOpenAIModels(models []llm.Model) []llm.Model {
	return enrichModels(models, func(id string) (int, int, string, bool) {
		id = strings.ToLower(id)
		switch {
		case strings.HasPrefix(id, "gpt-5.6-sol"), strings.HasPrefix(id, "gpt-5.4"):
			return 1_050_000, 128_000, "o200k_harmony", true
		case strings.HasPrefix(id, "gpt-5.2"), strings.HasPrefix(id, "gpt-5.1"), strings.HasPrefix(id, "gpt-5"):
			return 400_000, 128_000, "o200k_base", true
		case strings.HasPrefix(id, "gpt-4.1"):
			return 1_047_576, 32_768, "o200k_base", true
		case strings.HasPrefix(id, "gpt-4o"):
			return 128_000, 16_384, "o200k_base", true
		default:
			return 0, 0, "", false
		}
	})
}

// EnrichDeepSeekModels applies the published limits for built-in DeepSeek IDs.
func EnrichDeepSeekModels(models []llm.Model) []llm.Model {
	return enrichModels(models, func(id string) (int, int, string, bool) {
		id = strings.ToLower(id)
		switch {
		case strings.Contains(id, "v4"):
			return 1_000_000, 384_000, "deepseek_v4", true
		case strings.Contains(id, "v3"), strings.Contains(id, "deepseek-chat"), strings.Contains(id, "deepseek-reasoner"):
			return 128_000, 8_192, "deepseek_v3", true
		default:
			return 0, 0, "", false
		}
	})
}

func enrichModels(models []llm.Model, lookup func(string) (int, int, string, bool)) []llm.Model {
	values := append([]llm.Model(nil), models...)
	for index := range values {
		contextWindow, maxOutput, tokenizer, found := lookup(values[index].Ref.Model)
		if !found {
			continue
		}
		values[index].ContextWindow = contextWindow
		values[index].MaxOutput = maxOutput
		values[index].Tokenizer = llm.TokenizerMetadata{ID: tokenizer, Source: "builtin_versioned_catalog"}
	}
	return values
}
