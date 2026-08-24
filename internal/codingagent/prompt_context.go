package codingagent

import (
	"context"
	"fmt"

	"github.com/eaglc/codepilot/internal/llm"
)

func buildPromptContext(ctx context.Context, builder PromptBuilder, scope PromptScope) (string, []llm.Message, error) {
	systemPrompt, err := builder.BuildSystemPrompt(ctx, scope)
	if err != nil {
		return "", nil, err
	}
	untrustedBuilder, ok := builder.(UntrustedContextBuilder)
	if !ok {
		return systemPrompt, nil, nil
	}
	messages, err := untrustedBuilder.BuildUntrustedContext(ctx, scope)
	if err != nil {
		return "", nil, err
	}
	clones := make([]llm.Message, len(messages))
	for index := range messages {
		clones[index] = messages[index].Clone()
		if clones[index].Role != llm.RoleUser {
			return "", nil, fmt.Errorf("build Coding prompt: untrusted context message %d must use user role", index)
		}
		if err := clones[index].Validate(); err != nil {
			return "", nil, fmt.Errorf("build Coding prompt: untrusted context message %d: %w", index, err)
		}
	}
	return systemPrompt, clones, nil
}
