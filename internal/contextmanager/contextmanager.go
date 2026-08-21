// Package contextmanager defines the provider-neutral context processing
// pipeline applied immediately before a model invocation.
package contextmanager

import (
	"context"
	"errors"
	"fmt"
)

// Role identifies one provider-neutral conversation role.
type Role string

const (
	// RoleUser identifies user-authored context.
	RoleUser Role = "user"
	// RoleAssistant identifies assistant-authored context.
	RoleAssistant Role = "assistant"
)

// Scope contains immutable facts a strategy may use when choosing how to
// compress context. Strategies must not treat these values as mutable state.
type Scope struct {
	SessionID         string
	TurnID            string
	WorktreeRoot      string
	ProviderProfileID string
	ModelID           string
}

// Message is one provider-neutral message supplied to a context strategy.
// Current marks the user message that initiated the active turn.
type Message struct {
	ID      string
	Role    Role
	Content string
	Current bool
}

// Request is the complete input passed to each context strategy.
type Request struct {
	Scope        Scope
	SystemPrompt string
	Messages     []Message
}

// Result is the prompt and message sequence emitted by one strategy.
type Result struct {
	SystemPrompt string
	Messages     []Message
}

// Strategy transforms provider-neutral model context. Implementations may
// summarize, trim, or annotate context, but must preserve the current user
// message so the active request remains actionable.
type Strategy interface {
	Process(ctx context.Context, request Request) (Result, error)
}

// NopStrategy is the empty initial strategy and returns an isolated copy of
// its input without compressing or rewriting it.
type NopStrategy struct{}

// Process returns the supplied context unchanged.
func (NopStrategy) Process(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return Result{SystemPrompt: request.SystemPrompt, Messages: cloneMessages(request.Messages)}, nil
}

// Manager applies context strategies in registration order. Each strategy
// receives the prompt and messages returned by the preceding strategy.
type Manager struct {
	strategies []Strategy
}

// NewManager creates an ordered context processing pipeline.
func NewManager(strategies ...Strategy) (*Manager, error) {
	values := make([]Strategy, len(strategies))
	for index, strategy := range strategies {
		if strategy == nil {
			return nil, errors.New("create context manager: strategy is nil")
		}
		values[index] = strategy
	}
	return &Manager{strategies: values}, nil
}

// Process runs every configured strategy sequentially and returns an isolated
// copy so strategy-owned slices cannot mutate the invocation after return.
func (m *Manager) Process(ctx context.Context, request Request) (Result, error) {
	if m == nil {
		return Result{}, errors.New("process model context: manager is nil")
	}
	current := Result{SystemPrompt: request.SystemPrompt, Messages: cloneMessages(request.Messages)}
	for index, strategy := range m.strategies {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		result, err := strategy.Process(ctx, Request{
			Scope: request.Scope, SystemPrompt: current.SystemPrompt, Messages: cloneMessages(current.Messages),
		})
		if err != nil {
			return Result{}, fmt.Errorf("process model context: strategy %d: %w", index+1, err)
		}
		current = Result{SystemPrompt: result.SystemPrompt, Messages: cloneMessages(result.Messages)}
	}
	return Result{SystemPrompt: current.SystemPrompt, Messages: cloneMessages(current.Messages)}, nil
}

func cloneMessages(values []Message) []Message {
	return append([]Message(nil), values...)
}
