package roleprofile

import "github.com/eaglc/codepilot/internal/workflow"

// Validate returns the built-in read-only verification policy.
func Validate() Definition {
	allowed := append(readOnlyTools(), "list_check_plans", "run_checks")
	return Definition{
		Role: workflow.RoleValidate, Profile: ProfileValidate, PolicyVersion: 1,
		Prompt:   PromptPolicy{ReadOnly: true, Instructions: "Validate the delegated result against its acceptance criteria using only the available bounded checks. Report concrete validation evidence and do not repair failures."},
		Tools:    ToolPolicy{Allowed: allowed, NodeScoped: true},
		Result:   ResultPolicy{AllowChanges: false, RequireValidationOnSuccess: true},
		Workflow: WorkflowPolicy{Capability: workflow.CapabilityValidate, DefaultFailure: workflow.FailureBlock, DefaultAttempts: 1},
	}
}
