package contextmanager

import (
	"encoding/json"
	"testing"

	"github.com/eaglc/codepilot/internal/llm"
)

func TestMeasureReportsStableContentFreeCategories(t *testing.T) {
	messages := []Message{
		{Message: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "task"}}}, Current: true},
		{Message: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "instructions"}}}, Category: CategoryInstructions},
		{Message: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "skill"}}}, Category: CategorySkills},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "history"}}}},
		{Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call", ToolName: "read", Content: []llm.Content{{Type: llm.ContentText, Text: "preview"}}}},
		{Message: llm.Message{Role: llm.RoleTool, ToolCallID: "artifact", ToolName: "read", Content: []llm.Content{{Type: llm.ContentText, Text: "reference"}}}, Category: CategoryArtifacts},
	}
	tools := []llm.ToolDefinition{{Name: "read", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	statistics := Measure(ByteTokenizer{}, "trusted", messages, tools, Budget{ContextWindow: 1000, ReservedOutput: 100, SafetyMargin: 50, Source: "model_metadata"}, Policy{SummarizeThreshold: 680, HardLimit: 850})
	if len(statistics.Categories) != len(orderedCategories) || !statistics.Estimated || statistics.CountSource != "local_byte_estimate" {
		t.Fatalf("statistics = %#v", statistics)
	}
	inputTotal := 0
	for index, category := range statistics.Categories {
		if category.Category != orderedCategories[index] {
			t.Fatalf("category order = %#v", statistics.Categories)
		}
		if category.Category == CategoryReservedOutput {
			if category.Tokens != 100 || category.Items != 1 {
				t.Fatalf("reserved output = %#v", category)
			}
			continue
		}
		if category.Tokens <= 0 || category.Items <= 0 {
			t.Fatalf("missing category count = %#v", category)
		}
		inputTotal += category.Tokens
	}
	if statistics.InputTokens != inputTotal || statistics.InputBudget != 850 || statistics.SummarizeThreshold != 680 {
		t.Fatalf("budget/count mismatch = %#v", statistics)
	}
}
