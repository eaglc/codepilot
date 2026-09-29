package codingagent

import (
	"context"
	"fmt"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/llm"
)

func buildPromptContext(ctx context.Context, builder PromptBuilder, scope PromptScope) (string, []llm.Message, []agent.ContextCategory, error) {
	systemPrompt, err := builder.BuildSystemPrompt(ctx, scope)
	if err != nil {
		return "", nil, nil, err
	}
	var messages []llm.Message
	if instructionBuilder, ok := builder.(InstructionContextBuilder); ok {
		messages, _, err = instructionBuilder.BuildInstructionContext(ctx, scope)
	} else if untrustedBuilder, ok := builder.(UntrustedContextBuilder); ok {
		messages, err = untrustedBuilder.BuildUntrustedContext(ctx, scope)
	} else {
		return systemPrompt, nil, nil, nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	clones := make([]llm.Message, len(messages))
	for index := range messages {
		clones[index] = messages[index].Clone()
		if clones[index].Role != llm.RoleUser {
			return "", nil, nil, fmt.Errorf("build Coding prompt: untrusted context message %d must use user role", index)
		}
		if err := clones[index].Validate(); err != nil {
			return "", nil, nil, fmt.Errorf("build Coding prompt: untrusted context message %d: %w", index, err)
		}
	}
	categories := make([]agent.ContextCategory, len(clones))
	for index := range categories {
		categories[index] = agent.ContextInstructions
	}
	return systemPrompt, clones, categories, nil
}
