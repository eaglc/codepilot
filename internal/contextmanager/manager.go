// Package contextmanager selects and compacts provider-neutral model context.
package contextmanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/eaglc/codepilot/internal/llm"
)

// Scope contains stable identities used for summary generation and caching.
type Scope struct {
	SessionID string
	RunID     string
	Model     llm.ModelRef
}

// Message binds one canonical LLM message to durable conversation boundaries.
type Message struct {
	EntryID string
	TurnID  string
	Message llm.Message
	Current bool
}

// Request contains the full candidate context before strategy processing.
type Request struct {
	Scope        Scope
	SystemPrompt string
	Messages     []Message
	Tools        []llm.ToolDefinition
	Budget       Budget
}

// Result contains the selected context and any durable summaries generated during processing.
type Result struct {
	SystemPrompt string
	Messages     []Message
	Summaries    []Summary
	Degradations []Degradation
}

// Degradation records a safe context fallback without exposing Provider or
// storage implementation details.
type Degradation struct {
	Kind   string
	Reason string
}

// Strategy transforms one provider-neutral context view.
type Strategy interface {
	Process(ctx context.Context, request Request) (Result, error)
}

// Manager applies ordered context strategies without exposing Provider implementations.
type Manager struct {
	strategies []Strategy
}

// NewManager validates and creates a context strategy pipeline.
func NewManager(strategies ...Strategy) (*Manager, error) {
	for _, strategy := range strategies {
		if strategy == nil {
			return nil, errors.New("create context manager: strategy is nil")
		}
	}
	return &Manager{strategies: append([]Strategy(nil), strategies...)}, nil
}

// Process applies all configured strategies and defensively copies their results.
func (m *Manager) Process(ctx context.Context, request Request) (Result, error) {
	if m == nil {
		return Result{}, errors.New("process model context: manager is nil")
	}
	current := Result{SystemPrompt: request.SystemPrompt, Messages: cloneMessages(request.Messages)}
	for index, strategy := range m.strategies {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		result, err := strategy.Process(ctx, Request{Scope: request.Scope, SystemPrompt: current.SystemPrompt, Messages: current.Messages, Tools: cloneTools(request.Tools), Budget: request.Budget})
		if err != nil {
			return Result{}, fmt.Errorf("process model context strategy %d: %w", index+1, err)
		}
		current.SystemPrompt = result.SystemPrompt
		current.Messages = cloneMessages(result.Messages)
		current.Summaries = append(current.Summaries, cloneSummaries(result.Summaries)...)
		current.Degradations = append(current.Degradations, result.Degradations...)
	}
	return Result{SystemPrompt: current.SystemPrompt, Messages: cloneMessages(current.Messages), Summaries: cloneSummaries(current.Summaries), Degradations: append([]Degradation(nil), current.Degradations...)}, nil
}

func cloneTools(tools []llm.ToolDefinition) []llm.ToolDefinition {
	clones := make([]llm.ToolDefinition, len(tools))
	for index, definition := range tools {
		clones[index] = definition
		clones[index].InputSchema = append([]byte(nil), definition.InputSchema...)
	}
	return clones
}

func cloneMessages(messages []Message) []Message {
	clones := make([]Message, len(messages))
	for index, message := range messages {
		clones[index] = message
		clones[index].Message = message.Message.Clone()
	}
	return clones
}
