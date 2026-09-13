package roleprofile

import "github.com/eaglc/codepilot/internal/workflow"

// Integrate returns the P5 serial integration policy. Later isolated-worktree
// integration can version this definition without changing existing children.
func Integrate() Definition {
	return Definition{
		Role: workflow.RoleIntegrate, Profile: ProfileIntegrate, PolicyVersion: 1,
		Prompt:   PromptPolicy{Instructions: "Integrate only the explicitly supplied bounded results within the declared write scope. Check current workspace state, preserve unrelated changes, and report conflicts instead of widening scope."},
		Tools:    ToolPolicy{Allowed: implementationTools(), NodeScoped: true},
		Result:   ResultPolicy{AllowChanges: true},
		Workflow: WorkflowPolicy{Capability: workflow.CapabilityIntegrate, DefaultFailure: workflow.FailureTerminate, DefaultAttempts: 1},
	}
}
