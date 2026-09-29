package contextmanager

import (
	"strings"

	"github.com/eaglc/codepilot/internal/llm"
)

// Category identifies one stable, product-safe context ownership bucket.
type Category string

const (
	CategorySystem         Category = "system"
	CategoryTask           Category = "task"
	CategoryInstructions   Category = "instructions"
	CategorySkills         Category = "skills"
	CategoryHistory        Category = "history"
	CategoryToolResults    Category = "tool_results"
	CategoryArtifacts      Category = "artifacts"
	CategoryReservedOutput Category = "reserved_output"
)

var orderedCategories = []Category{
	CategorySystem,
	CategoryTask,
	CategoryInstructions,
	CategorySkills,
	CategoryHistory,
	CategoryToolResults,
	CategoryArtifacts,
	CategoryReservedOutput,
}

// CategoryStatistics reports a bounded count without carrying source content.
type CategoryStatistics struct {
	Category Category
	Tokens   int
	Items    int
	Source   string
}

// Statistics describes the exact selected request shape using an explicitly
// identified counting source. Category counts are local estimates unless a
// future Provider supplies category-level accounting.
type Statistics struct {
	CountSource        string
	Estimated          bool
	InputTokens        int
	ContextWindow      int
	InputBudget        int
	ReservedOutput     int
	SafetyMargin       int
	SummarizeThreshold int
	HardLimit          int
	BudgetSource       string
	Compacted          bool
	Categories         []CategoryStatistics
}

// Clone returns a defensive copy of context statistics.
func (s Statistics) Clone() Statistics {
	s.Categories = append([]CategoryStatistics(nil), s.Categories...)
	return s
}

type statisticsSource interface {
	statisticsPolicy(Budget) (Tokenizer, Policy)
}

// Measure counts a selected provider-neutral request without retaining any of
// its text, tool schemas, paths, or other source data.
func Measure(tokenizer Tokenizer, systemPrompt string, messages []Message, tools []llm.ToolDefinition, budget Budget, policy Policy) Statistics {
	if tokenizer == nil {
		tokenizer = ByteTokenizer{}
	}
	statistics := Statistics{
		CountSource: "local_byte_estimate", Estimated: true,
		ContextWindow: budget.ContextWindow, ReservedOutput: budget.ReservedOutput,
		SafetyMargin: budget.SafetyMargin, SummarizeThreshold: policy.SummarizeThreshold,
		HardLimit: policy.HardLimit, BudgetSource: strings.TrimSpace(budget.Source),
		Categories: make([]CategoryStatistics, len(orderedCategories)),
	}
	statistics.InputBudget = policy.HardLimit
	if statistics.InputBudget <= 0 && budget.ContextWindow > 0 {
		statistics.InputBudget = budget.ContextWindow - budget.ReservedOutput - budget.SafetyMargin
	}
	for index, category := range orderedCategories {
		statistics.Categories[index] = CategoryStatistics{Category: category, Source: categorySource(category)}
	}
	add := func(category Category, tokens int) {
		for index := range statistics.Categories {
			if statistics.Categories[index].Category == category {
				statistics.Categories[index].Tokens += max(0, tokens)
				statistics.Categories[index].Items++
				return
			}
		}
	}
	if systemPrompt != "" {
		add(CategorySystem, tokenizer.CountText(systemPrompt))
	}
	for _, definition := range tools {
		add(CategorySystem, tokenizer.CountTool(definition))
	}
	for _, message := range messages {
		add(messageCategory(message), tokenizer.CountMessage(message.Message))
	}
	if budget.ReservedOutput > 0 {
		add(CategoryReservedOutput, budget.ReservedOutput)
	}
	for _, category := range statistics.Categories {
		if category.Category != CategoryReservedOutput {
			statistics.InputTokens += category.Tokens
		}
	}
	return statistics
}

// Recount recalculates category sizes after the final security transform while
// retaining the budget and compaction facts selected by the manager.
func Recount(base Statistics, systemPrompt string, messages []Message, tools []llm.ToolDefinition) Statistics {
	policy := Policy{SummarizeThreshold: base.SummarizeThreshold, HardLimit: base.HardLimit}
	budget := Budget{ContextWindow: base.ContextWindow, ReservedOutput: base.ReservedOutput, SafetyMargin: base.SafetyMargin, Source: base.BudgetSource}
	statistics := Measure(ByteTokenizer{}, systemPrompt, messages, tools, budget, policy)
	statistics.Compacted = base.Compacted
	return statistics
}

func messageCategory(message Message) Category {
	switch message.Category {
	case CategoryTask, CategoryInstructions, CategorySkills, CategoryHistory, CategoryToolResults, CategoryArtifacts:
		return message.Category
	}
	if message.Current {
		return CategoryTask
	}
	if message.Message.Role == llm.RoleTool {
		return CategoryToolResults
	}
	return CategoryHistory
}

func categorySource(category Category) string {
	switch category {
	case CategorySystem:
		return "trusted prompt and tool schemas"
	case CategoryTask:
		return "current task context"
	case CategoryInstructions:
		return "project instruction context"
	case CategorySkills:
		return "active skill context"
	case CategoryHistory:
		return "selected conversation history"
	case CategoryToolResults:
		return "selected tool result previews"
	case CategoryArtifacts:
		return "selected artifact references"
	case CategoryReservedOutput:
		return "model output reservation"
	default:
		return "selected context"
	}
}
