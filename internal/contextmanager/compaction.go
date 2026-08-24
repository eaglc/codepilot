package contextmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eaglc/codepilot/internal/llm"
)

const (
	compactionStrategy        = "rolling-summary"
	compactionStrategyVersion = "v4"
)

// Budget contains model-specific input bounds. Zero values retain the
// strategy's conservative fallback policy.
type Budget struct {
	ContextWindow  int
	ReservedOutput int
	SafetyMargin   int
	Source         string
}

// BudgetForModel reserves the model's maximum output and an additional margin
// for provider framing that a local tokenizer may not count.
func BudgetForModel(model llm.Model) Budget {
	if model.ContextWindow <= 0 {
		return Budget{}
	}
	output := model.MaxOutput
	if output <= 0 {
		output = model.ContextWindow / 8
	}
	margin := model.ContextWindow / 20
	if margin < 1024 {
		margin = 1024
	}
	return Budget{ContextWindow: model.ContextWindow, ReservedOutput: output, SafetyMargin: margin, Source: "model_metadata"}
}

// CurrentTurnTooLargeError reports that no history-only compaction can make a
// structurally valid request fit the selected model.
type CurrentTurnTooLargeError struct {
	Tokens int
	Limit  int
}

func (e *CurrentTurnTooLargeError) Error() string {
	return fmt.Sprintf("current turn requires %d estimated input tokens, exceeding the model input budget of %d; shorten the prompt or tool output, or select a model with a larger context window", e.Tokens, e.Limit)
}

// Policy controls when and how much history a compaction keeps.
type Policy struct {
	RecentTurns        int
	SummarizeThreshold int
	HardLimit          int
}

// Validate checks compaction bounds.
func (p Policy) Validate() error {
	if p.RecentTurns < 1 {
		return errors.New("recent turns must be at least one")
	}
	if p.SummarizeThreshold < 1 || p.HardLimit < 1 || p.SummarizeThreshold >= p.HardLimit {
		return errors.New("summarize threshold must be positive and lower than hard limit")
	}
	return nil
}

// CompactionStrategy summarizes old complete turns and retains a recent full tail.
type CompactionStrategy struct {
	policy     Policy
	tokenizer  Tokenizer
	summarizer Summarizer
	store      SummaryStore
	sanitizer  TextSanitizer
}

// NewCompactionStrategy creates a model-neutral rolling-summary strategy.
func NewCompactionStrategy(policy Policy, tokenizer Tokenizer, summarizer Summarizer, store SummaryStore, sanitizers ...TextSanitizer) (*CompactionStrategy, error) {
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("create compaction strategy: %w", err)
	}
	if tokenizer == nil {
		return nil, errors.New("create compaction strategy: tokenizer is nil")
	}
	if summarizer == nil {
		return nil, errors.New("create compaction strategy: summarizer is nil")
	}
	if store == nil {
		return nil, errors.New("create compaction strategy: summary store is nil")
	}
	if len(sanitizers) > 1 {
		return nil, errors.New("create compaction strategy: at most one text sanitizer is allowed")
	}
	sanitizer := TextSanitizer(identityTextSanitizer{})
	if len(sanitizers) == 1 {
		if sanitizers[0] == nil {
			return nil, errors.New("create compaction strategy: text sanitizer is nil")
		}
		sanitizer = sanitizers[0]
	}
	return &CompactionStrategy{policy: policy, tokenizer: tokenizer, summarizer: summarizer, store: store, sanitizer: sanitizer}, nil
}

// Process compacts only when the complete request exceeds the configured threshold.
func (s *CompactionStrategy) Process(ctx context.Context, request Request) (Result, error) {
	if s == nil {
		return Result{}, errors.New("process context compaction: strategy is nil")
	}
	currentIndex, err := currentMessageIndex(request.Messages)
	if err != nil {
		return Result{}, err
	}
	policy := s.effectivePolicy(request.Budget)
	if contextTokens(s.tokenizer, request.SystemPrompt, request.Messages, request.Tools) <= policy.SummarizeThreshold {
		return Result{SystemPrompt: request.SystemPrompt, Messages: cloneMessages(request.Messages)}, nil
	}
	history := request.Messages[:currentIndex]
	turns := groupTurns(history)
	cut := len(turns) - s.policy.RecentTurns
	if cut <= 0 {
		messages, err := fitHardLimit(s.tokenizer, request.SystemPrompt, request.Messages, request.Tools, policy.HardLimit)
		return Result{SystemPrompt: request.SystemPrompt, Messages: messages}, err
	}
	old := flattenTurns(turns[:cut])
	tail := flattenTurns(turns[cut:])
	digest, err := sourceDigest(old)
	if err != nil {
		return Result{}, err
	}
	key := summaryKey(request.Scope.SessionID, digest, compactionStrategy, compactionStrategyVersion)
	facts := ExtractSummaryFacts(old)
	summary, found, err := s.store.LoadSummary(ctx, key)
	var degradations []Degradation
	if err != nil {
		found = false
		degradations = append(degradations, Degradation{Kind: "summary_cache_unavailable", Reason: "Stored summary could not be loaded; a new summary was attempted."})
	}
	if !found {
		output, err := s.summarizer.Summarize(ctx, SummaryRequest{
			Scope: request.Scope, Messages: cloneMessages(old), SourceDigest: digest, Strategy: compactionStrategy, Version: compactionStrategyVersion,
		})
		output.Text = strings.TrimSpace(s.sanitizer.SanitizeText(output.Text))
		if err != nil || output.Text == "" || ValidateSummaryFacts(output.Text, facts) != nil {
			return s.safeTrim(request, tail, currentIndex, policy, "Summary generation failed or omitted required facts; oldest complete turns were safely trimmed.", degradations)
		}
		summary = Summary{
			Text: strings.TrimSpace(output.Text), CoversFromEntryID: old[0].EntryID, CoversToEntryID: old[len(old)-1].EntryID,
			SourceDigest: digest, Strategy: compactionStrategy, StrategyVersion: compactionStrategyVersion, Model: output.Model, Usage: output.Usage, Facts: facts,
		}
		if err := s.store.SaveSummary(ctx, key, summary); err != nil {
			degradations = append(degradations, Degradation{Kind: "summary_cache_unavailable", Reason: "Summary cache could not be saved; the durable Agent compaction entry remains authoritative."})
		}
	} else {
		summary.Text = strings.TrimSpace(s.sanitizer.SanitizeText(summary.Text))
		if summary.Text == "" || ValidateSummaryFacts(summary.Text, facts) != nil {
			return s.safeTrim(request, tail, currentIndex, policy, "Stored summary failed sanitization or fact validation; oldest complete turns were safely trimmed.", degradations)
		}
	}
	summaryMessage, err := summaryContextMessage(summary)
	if err != nil {
		return s.safeTrim(request, tail, currentIndex, policy, "Stored summary could not be isolated as untrusted context data; oldest complete turns were safely trimmed.", degradations)
	}
	messages := append([]Message{summaryMessage}, cloneMessages(tail)...)
	messages = append(messages, request.Messages[currentIndex])
	messages, err = fitHardLimit(s.tokenizer, request.SystemPrompt, messages, request.Tools, policy.HardLimit)
	if err != nil {
		return Result{}, err
	}
	return Result{SystemPrompt: request.SystemPrompt, Messages: messages, Summaries: []Summary{summary}, Degradations: degradations}, nil
}

func (s *CompactionStrategy) safeTrim(request Request, tail []Message, currentIndex int, policy Policy, reason string, existing []Degradation) (Result, error) {
	messages := append(cloneMessages(tail), request.Messages[currentIndex])
	messages, err := fitHardLimit(s.tokenizer, request.SystemPrompt, messages, request.Tools, policy.HardLimit)
	if err != nil {
		return Result{}, err
	}
	degradations := append([]Degradation(nil), existing...)
	degradations = append(degradations, Degradation{Kind: "summary_safe_trim", Reason: reason})
	return Result{SystemPrompt: request.SystemPrompt, Messages: messages, Degradations: degradations}, nil
}

func (s *CompactionStrategy) effectivePolicy(budget Budget) Policy {
	policy := s.policy
	if budget.ContextWindow <= 0 {
		return policy
	}
	hard := budget.ContextWindow - budget.ReservedOutput - budget.SafetyMargin
	if hard < 2 {
		return policy
	}
	policy.HardLimit = hard
	policy.SummarizeThreshold = hard * 4 / 5
	if policy.SummarizeThreshold >= policy.HardLimit {
		policy.SummarizeThreshold = policy.HardLimit - 1
	}
	return policy
}

type turnBlock struct {
	messages []Message
}

func groupTurns(messages []Message) []turnBlock {
	var turns []turnBlock
	for _, message := range messages {
		if len(turns) == 0 || message.TurnID == "" || turns[len(turns)-1].messages[0].TurnID != message.TurnID {
			turns = append(turns, turnBlock{})
		}
		turns[len(turns)-1].messages = append(turns[len(turns)-1].messages, message)
	}
	return turns
}

func flattenTurns(turns []turnBlock) []Message {
	var messages []Message
	for _, turn := range turns {
		messages = append(messages, turn.messages...)
	}
	return cloneMessages(messages)
}

func currentMessageIndex(messages []Message) (int, error) {
	index := -1
	for position, message := range messages {
		if !message.Current {
			continue
		}
		if index != -1 {
			return -1, errors.New("process context: multiple current messages")
		}
		index = position
	}
	if index == -1 || index != len(messages)-1 {
		return -1, errors.New("process context: current message must be present and last")
	}
	return index, nil
}

func contextTokens(tokenizer Tokenizer, systemPrompt string, messages []Message, tools []llm.ToolDefinition) int {
	total := tokenizer.CountText(systemPrompt)
	for _, message := range messages {
		total += tokenizer.CountMessage(message.Message)
	}
	for _, definition := range tools {
		total += tokenizer.CountTool(definition)
	}
	return total
}

func fitHardLimit(tokenizer Tokenizer, systemPrompt string, messages []Message, tools []llm.ToolDefinition, limit int) ([]Message, error) {
	result := cloneMessages(messages)
	currentTurnID := result[len(result)-1].TurnID
	for len(result) > 1 && contextTokens(tokenizer, systemPrompt, result, tools) > limit {
		if currentTurnID != "" && result[0].TurnID == currentTurnID {
			break
		}
		turnID := result[0].TurnID
		cut := 1
		if turnID != "" {
			for cut < len(result)-1 && result[cut].TurnID == turnID {
				cut++
			}
		}
		result = result[cut:]
	}
	used := contextTokens(tokenizer, systemPrompt, result, tools)
	if used > limit {
		return nil, &CurrentTurnTooLargeError{Tokens: used, Limit: limit}
	}
	return cloneMessages(result), nil
}
