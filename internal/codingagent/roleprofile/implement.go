package roleprofile

import "github.com/eaglc/codepilot/internal/workflow"

// Implement returns the built-in bounded modification policy.
func Implement() Definition {
	return Definition{
		Role: workflow.RoleImplement, Profile: ProfileImplement, PolicyVersion: 1,
		Prompt:   PromptPolicy{Instructions: "Implement only the delegated change within its declared write scope. Preserve unrelated user work and validate the resulting change before submitting evidence."},
		Tools:    ToolPolicy{Allowed: implementationTools(), NodeScoped: true},
		Result:   ResultPolicy{AllowChanges: true},
		Workflow: WorkflowPolicy{Capability: workflow.CapabilityImplement, DefaultFailure: workflow.FailureRetry, DefaultAttempts: 2},
	}
}
