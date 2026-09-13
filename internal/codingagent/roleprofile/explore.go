package roleprofile

import "github.com/eaglc/codepilot/internal/workflow"

// Explore returns the built-in read-only investigation policy.
func Explore() Definition {
	return Definition{
		Role: workflow.RoleExplore, Profile: ProfileExplore, PolicyVersion: 1,
		Prompt:   PromptPolicy{ReadOnly: true, Instructions: "Explore only the bounded task scope. Report evidence-backed findings and unresolved questions without proposing workspace changes as completed work."},
		Tools:    ToolPolicy{Allowed: readOnlyTools(), NodeScoped: true},
		Result:   ResultPolicy{AllowChanges: false},
		Workflow: WorkflowPolicy{Capability: workflow.CapabilityExplore, DefaultFailure: workflow.FailureReplan, DefaultAttempts: 1},
	}
}
