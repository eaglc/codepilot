package agent

import (
	"errors"
	"fmt"
	"strings"
)

func buildSystemPrompt(profile LanguageProfile) (string, error) {
	if profile.ID == "" || strings.TrimSpace(profile.PromptHint) == "" {
		return "", errors.New("build system prompt: language profile is incomplete")
	}
	var prompt strings.Builder
	prompt.WriteString("You are CodePilot, a coding agent operating on one trusted Git worktree.\n\n")
	prompt.WriteString("Rules:\n")
	prompt.WriteString("- Use only the registered tools. Never claim a file change or check result without tool evidence.\n")
	prompt.WriteString("- Inspect relevant files before editing. Keep patches minimal and preserve unrelated user changes.\n")
	prompt.WriteString("- If the user asks for explanation, review, or diagnosis without requesting a change, inspect as needed and answer directly without editing files.\n")
	prompt.WriteString("- Apply edits only through apply_patch. Run checks only through a listed run_checks plan.\n")
	prompt.WriteString("- Patch and check tools may pause for user approval. Respect denials and cancellation.\n")
	prompt.WriteString("- Do not declare the turn verified; verification status is derived outside the model from applied patches and check evidence.\n")
	prompt.WriteString("- Finish with a concise answer; for change requests, summarize actual changes and checks.\n\n")
	prompt.WriteString("Language guidance: ")
	prompt.WriteString(strings.TrimSpace(profile.PromptHint))
	prompt.WriteString("\n")
	if len(profile.CheckPlans) == 0 {
		prompt.WriteString("Trusted check plans: none.\n")
	} else {
		prompt.WriteString("Trusted check plans:\n")
		for _, plan := range profile.CheckPlans {
			if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.Description) == "" {
				return "", errors.New("build system prompt: language check plan is invalid")
			}
			fmt.Fprintf(&prompt, "- %s: %s\n", plan.ID, strings.TrimSpace(plan.Description))
		}
	}
	if prompt.Len() > maxSystemPromptBytes {
		return "", errors.New("build system prompt: prompt exceeds its size limit")
	}
	return prompt.String(), nil
}
