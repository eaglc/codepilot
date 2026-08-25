package contextmanager

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/llm"
)

const (
	compactionStrategy        = "rolling-summary"
	compactionStrategyVersion = "v6"
	maxSummaryMergeLevels     = 8
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
	visible, err := contextWithPriorSummary(request.PriorSummary, request.Messages)
	if err != nil {
		return Result{}, err
	}
	if contextTokens(s.tokenizer, request.SystemPrompt, visible, request.Tools) <= policy.SummarizeThreshold {
		return Result{SystemPrompt: request.SystemPrompt, Messages: visible}, nil
	}
	var history, ephemeral []Message
	for _, message := range request.Messages[:currentIndex] {
		if message.Ephemeral {
			ephemeral = append(ephemeral, message)
			continue
		}
		history = append(history, message)
	}
	turns := groupTurns(history)
	cut := len(turns) - s.policy.RecentTurns
	if cut <= 0 {
		messages, err := fitHardLimit(s.tokenizer, request.SystemPrompt, visible, request.Tools, policy.HardLimit)
		return Result{SystemPrompt: request.SystemPrompt, Messages: messages}, err
	}
	newOld := flattenTurns(turns[:cut])
	tail := flattenTurns(turns[cut:])
	old := cloneMessages(newOld)
	fromEntryID := newOld[0].EntryID
	facts := ExtractSummaryFacts(newOld)
	if request.PriorSummary != nil {
		priorMessage, err := summaryContextMessage(*request.PriorSummary)
		if err != nil {
			return s.safeTrim(request, tail, ephemeral, currentIndex, policy, "Stored summary could not be isolated as untrusted context data; oldest complete turns were safely trimmed.", nil, nil)
		}
		old = append([]Message{priorMessage}, old...)
		fromEntryID = request.PriorSummary.CoversFromEntryID
		facts = mergeSummaryFacts(request.PriorSummary.Facts, facts)
	}
	digest, err := sourceDigest(old)
	if err != nil {
		return Result{}, err
	}
	if request.OnCompactionStarted != nil {
		boundary := CompactionBoundary{SourceDigest: digest, FromEntryID: fromEntryID, ToEntryID: newOld[len(newOld)-1].EntryID}
		if err := request.OnCompactionStarted(ctx, boundary); err != nil {
			return Result{}, fmt.Errorf("publish context compaction start: %w", err)
		}
	}
	key := typedSummaryKey(request.Scope.SessionID, digest, compactionStrategy, compactionStrategyVersion, SummaryKindFinal)
	summary, found, err := s.store.LoadSummary(ctx, key)
	var degradations []Degradation
	var summaryUsage []llm.Usage
	if err != nil {
		found = false
		degradations = append(degradations, Degradation{Kind: "summary_cache_unavailable", Reason: "Stored summary could not be loaded; a new summary was attempted."})
	}
	if !found {
		summary, err = s.summarizeHierarchy(ctx, request.Scope, old, facts, fromEntryID, newOld[len(newOld)-1].EntryID, digest, policy.HardLimit, &degradations, &summaryUsage)
		if err != nil {
			return s.safeTrim(request, tail, ephemeral, currentIndex, policy, "Summary generation failed or omitted required facts; oldest complete turns were safely trimmed.", degradations, summaryUsage)
		}
	} else {
		summary.Text = strings.TrimSpace(s.sanitizer.SanitizeText(summary.Text))
		if summary.Kind != "" && summary.Kind != SummaryKindFinal || summary.SourceDigest != digest || summary.Text == "" || ValidateSummaryFacts(summary.Text, facts) != nil {
			return s.safeTrim(request, tail, ephemeral, currentIndex, policy, "Stored summary failed sanitization or fact validation; oldest complete turns were safely trimmed.", degradations, nil)
		}
	}
	summaryMessage, err := summaryContextMessage(summary)
	if err != nil {
		return s.safeTrim(request, tail, ephemeral, currentIndex, policy, "Stored summary could not be isolated as untrusted context data; oldest complete turns were safely trimmed.", degradations, nil)
	}
	messages := append([]Message{summaryMessage}, cloneMessages(tail)...)
	messages = append(messages, cloneMessages(ephemeral)...)
	messages = append(messages, request.Messages[currentIndex])
	messages, err = fitHardLimit(s.tokenizer, request.SystemPrompt, messages, request.Tools, policy.HardLimit)
	if err != nil {
		return Result{SummaryUsage: append([]llm.Usage(nil), summaryUsage...)}, err
	}
	retained := containsMessageEntry(messages, summaryMessage.EntryID)
	if !retained {
		degradations = append(degradations, Degradation{Kind: "summary_hard_trimmed", Reason: "The generated summary could not fit beside the current turn and was not made authoritative."})
		return Result{SystemPrompt: request.SystemPrompt, Messages: messages, SummaryUsage: summaryUsage, Degradations: degradations}, nil
	}
	return Result{SystemPrompt: request.SystemPrompt, Messages: messages, Summaries: []Summary{summary}, SummaryUsage: summaryUsage, Degradations: degradations}, nil
}

func (s *CompactionStrategy) safeTrim(request Request, tail, ephemeral []Message, currentIndex int, policy Policy, reason string, existing []Degradation, summaryUsage []llm.Usage) (Result, error) {
	var messages []Message
	if request.PriorSummary != nil {
		if prior, err := summaryContextMessage(*request.PriorSummary); err == nil {
			messages = append(messages, prior)
		}
	}
	messages = append(messages, cloneMessages(tail)...)
	messages = append(messages, cloneMessages(ephemeral)...)
	messages = append(messages, request.Messages[currentIndex])
	messages, err := fitHardLimit(s.tokenizer, request.SystemPrompt, messages, request.Tools, policy.HardLimit)
	if err != nil {
		return Result{SummaryUsage: append([]llm.Usage(nil), summaryUsage...)}, err
	}
	degradations := append([]Degradation(nil), existing...)
	degradations = append(degradations, Degradation{Kind: "summary_safe_trim", Reason: reason})
	return Result{SystemPrompt: request.SystemPrompt, Messages: messages, SummaryUsage: append([]llm.Usage(nil), summaryUsage...), Degradations: degradations}, nil
}

func contextWithPriorSummary(prior *Summary, messages []Message) ([]Message, error) {
	result := cloneMessages(messages)
	if prior == nil {
		return result, nil
	}
	message, err := summaryContextMessage(*prior)
	if err != nil {
		return nil, err
	}
	return append([]Message{message}, result...), nil
}

func (s *CompactionStrategy) summarizeHierarchy(ctx context.Context, scope Scope, messages []Message, facts []SummaryFact, fromEntryID, toEntryID, digest string, limit int, degradations *[]Degradation, usages *[]llm.Usage) (Summary, error) {
	chunks, err := splitSummaryChunks(s.tokenizer, messages, limit)
	if err != nil {
		return Summary{}, err
	}
	if len(chunks) == 1 {
		return s.loadOrCreateSummary(ctx, scope, chunks[0], facts, fromEntryID, toEntryID, digest, SummaryKindFinal, limit, degradations, usages)
	}
	partials := make([]Summary, 0, len(chunks))
	for _, chunk := range chunks {
		chunkDigest, err := sourceDigest(chunk)
		if err != nil {
			return Summary{}, err
		}
		chunkFacts := ExtractSummaryFacts(chunk)
		partial, err := s.loadOrCreateSummary(ctx, scope, chunk, chunkFacts, chunk[0].EntryID, chunk[len(chunk)-1].EntryID, chunkDigest, SummaryKindChunk, limit, degradations, usages)
		if err != nil {
			return Summary{}, err
		}
		partials = append(partials, partial)
	}
	for level := 0; len(partials) > 1; level++ {
		if level >= maxSummaryMergeLevels {
			return Summary{}, errors.New("summarize context: merge depth exceeded")
		}
		mergeMessages, err := summaryMessages(partials)
		if err != nil {
			return Summary{}, err
		}
		mergeChunks, err := splitSummaryChunks(s.tokenizer, mergeMessages, limit)
		if err != nil {
			return Summary{}, err
		}
		if len(mergeChunks) == 1 {
			return s.loadOrCreateSummary(ctx, scope, mergeChunks[0], facts, fromEntryID, toEntryID, digest, SummaryKindFinal, limit, degradations, usages)
		}
		if len(mergeChunks) >= len(partials) {
			return Summary{}, errors.New("summarize context: intermediate summaries did not fit a smaller merge level")
		}
		next := make([]Summary, 0, len(mergeChunks))
		for _, chunk := range mergeChunks {
			chunkDigest, err := sourceDigest(chunk)
			if err != nil {
				return Summary{}, err
			}
			chunkFacts := ExtractSummaryFacts(chunk)
			merged, err := s.loadOrCreateSummary(ctx, scope, chunk, chunkFacts, chunk[0].EntryID, chunk[len(chunk)-1].EntryID, chunkDigest, SummaryKindMerge, limit, degradations, usages)
			if err != nil {
				return Summary{}, err
			}
			next = append(next, merged)
		}
		partials = next
	}
	return Summary{}, errors.New("summarize context: no final summary was produced")
}

func (s *CompactionStrategy) loadOrCreateSummary(ctx context.Context, scope Scope, messages []Message, facts []SummaryFact, fromEntryID, toEntryID, digest string, kind SummaryKind, limit int, degradations *[]Degradation, usages *[]llm.Usage) (Summary, error) {
	key := typedSummaryKey(scope.SessionID, digest, compactionStrategy, compactionStrategyVersion, kind)
	maxOutputTokens := summaryOutputLimit(limit)
	if cached, found, err := s.store.LoadSummary(ctx, key); err == nil && found {
		cached.Text = strings.TrimSpace(s.sanitizer.SanitizeText(cached.Text))
		if cached.SourceDigest == digest && cached.Text != "" && s.tokenizer.CountText(cached.Text) <= maxOutputTokens && ValidateSummaryFacts(cached.Text, facts) == nil {
			return cached, nil
		}
	} else if err != nil {
		*degradations = append(*degradations, Degradation{Kind: "summary_cache_unavailable", Reason: "An intermediate summary cache entry could not be loaded; it was regenerated."})
	}
	output, err := s.summarizer.Summarize(ctx, SummaryRequest{Scope: scope, Messages: cloneMessages(messages), SourceDigest: digest, Strategy: compactionStrategy, Version: compactionStrategyVersion, MaxOutputTokens: maxOutputTokens})
	if output.Usage != nil {
		*usages = append(*usages, *output.Usage)
	}
	if err != nil {
		return Summary{}, err
	}
	output.Text = strings.TrimSpace(s.sanitizer.SanitizeText(output.Text))
	if output.Text == "" {
		return Summary{}, errors.New("summarize context: empty summary")
	}
	if s.tokenizer.CountText(output.Text) > maxOutputTokens {
		return Summary{}, errors.New("summarize context: generated summary exceeded its output budget")
	}
	if err := ValidateSummaryFacts(output.Text, facts); err != nil {
		return Summary{}, err
	}
	summary := Summary{Kind: kind, Text: output.Text, CoversFromEntryID: fromEntryID, CoversToEntryID: toEntryID, SourceDigest: digest, Strategy: compactionStrategy, StrategyVersion: compactionStrategyVersion, Model: output.Model, Usage: output.Usage, Facts: append([]SummaryFact(nil), facts...)}
	if err := s.store.SaveSummary(ctx, key, summary); err != nil {
		*degradations = append(*degradations, Degradation{Kind: "summary_cache_unavailable", Reason: "Summary cache could not be saved; the durable Agent compaction entry remains authoritative."})
	}
	return summary, nil
}

func summaryOutputLimit(inputLimit int) int {
	limit := inputLimit / 8
	if limit < 1 {
		limit = 1
	}
	return effectiveSummaryMaxOutputTokens(limit)
}

func splitSummaryChunks(tokenizer Tokenizer, messages []Message, limit int) ([][]Message, error) {
	if len(messages) == 0 {
		return nil, errors.New("summarize context: source messages are empty")
	}
	turns := groupTurns(messages)
	var chunks [][]Message
	var current []Message
	for _, turn := range turns {
		candidate := append(cloneMessages(current), turn.messages...)
		candidateTokens, err := summaryRequestTokens(tokenizer, candidate)
		if err != nil {
			return nil, err
		}
		if len(current) != 0 && candidateTokens > limit {
			chunks = append(chunks, current)
			current = nil
		}
		turnTokens, err := summaryRequestTokens(tokenizer, turn.messages)
		if err != nil {
			return nil, err
		}
		if turnTokens > limit {
			if len(turn.messages) == 1 {
				return nil, errors.New("summarize context: one source message exceeds the summary input limit")
			}
			for _, message := range turn.messages {
				messageTokens, err := summaryRequestTokens(tokenizer, []Message{message})
				if err != nil {
					return nil, err
				}
				if messageTokens > limit {
					return nil, errors.New("summarize context: one source message exceeds the summary input limit")
				}
				combinedTokens, err := summaryRequestTokens(tokenizer, append(cloneMessages(current), message))
				if err != nil {
					return nil, err
				}
				if len(current) != 0 && combinedTokens > limit {
					chunks = append(chunks, current)
					current = nil
				}
				current = append(current, message)
			}
			continue
		}
		current = append(current, turn.messages...)
	}
	if len(current) != 0 {
		chunks = append(chunks, current)
	}
	return chunks, nil
}

func summaryRequestTokens(tokenizer Tokenizer, messages []Message) (int, error) {
	input, err := FormatSummaryInput(messages)
	if err != nil {
		return 0, err
	}
	requestMessage := llm.Message{
		Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: input}},
		Timestamp: time.Unix(0, 123456789).UTC(),
	}
	return tokenizer.CountText(SummarySystemPrompt) + tokenizer.CountMessage(requestMessage), nil
}

func summaryMessages(values []Summary) ([]Message, error) {
	messages := make([]Message, 0, len(values))
	for _, value := range values {
		message, err := summaryContextMessage(value)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func mergeSummaryFacts(groups ...[]SummaryFact) []SummaryFact {
	unique := make(map[SummaryFact]struct{})
	for _, group := range groups {
		for _, fact := range group {
			unique[fact] = struct{}{}
		}
	}
	result := make([]SummaryFact, 0, len(unique))
	for fact := range unique {
		result = append(result, fact)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Kind == result[right].Kind {
			return result[left].Value < result[right].Value
		}
		return result[left].Kind < result[right].Kind
	})
	return result
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
		start, end, found := oldestDroppableTurn(result, currentTurnID, false)
		if !found {
			start, end, found = oldestDroppableTurn(result, currentTurnID, true)
		}
		if !found {
			break
		}
		result = append(result[:start], result[end:]...)
	}
	used := contextTokens(tokenizer, systemPrompt, result, tools)
	if used > limit {
		return nil, &CurrentTurnTooLargeError{Tokens: used, Limit: limit}
	}
	return cloneMessages(result), nil
}

func oldestDroppableTurn(messages []Message, currentTurnID string, allowSummary bool) (int, int, bool) {
	for start := 0; start < len(messages)-1; {
		turnID := messages[start].TurnID
		end := start + 1
		if turnID != "" {
			for end < len(messages)-1 && messages[end].TurnID == turnID {
				end++
			}
		}
		if currentTurnID != "" && turnID == currentTurnID {
			return 0, 0, false
		}
		derivedSummary := false
		for index := start; index < end; index++ {
			derivedSummary = derivedSummary || messages[index].DerivedSummary
		}
		if allowSummary || !derivedSummary {
			return start, end, true
		}
		start = end
	}
	return 0, 0, false
}

func containsMessageEntry(messages []Message, entryID string) bool {
	for _, message := range messages {
		if message.EntryID == entryID {
			return true
		}
	}
	return false
}
