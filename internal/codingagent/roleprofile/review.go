package roleprofile

import "github.com/eaglc/codepilot/internal/workflow"

// Review returns the built-in read-only review policy.
func Review() Definition {
	return Definition{
		Role: workflow.RoleReview, Profile: ProfileReview, PolicyVersion: 1,
		Prompt:   PromptPolicy{ReadOnly: true, Instructions: "Review the bounded change and supplied evidence against the approved criteria. Report defects, risks, and unresolved issues without modifying the workspace."},
		Tools:    ToolPolicy{Allowed: readOnlyTools(), NodeScoped: true},
		Result:   ResultPolicy{AllowChanges: false},
		Workflow: WorkflowPolicy{Capability: workflow.CapabilityReview, DefaultFailure: workflow.FailureReplan, DefaultAttempts: 1},
	}
}
