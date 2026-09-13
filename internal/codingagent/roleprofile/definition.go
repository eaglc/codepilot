// Package roleprofile defines the immutable, trusted policies attached to
// bounded Coding Agent roles. It contains declarations only; concrete tools
// remain owned by the Coding tool factory.
package roleprofile

import "github.com/eaglc/codepilot/internal/workflow"

// Profile is the stable capability identity used to construct prompts and
// tool registries for one Workflow node or child Agent.
type Profile string

const (
	ProfileExplore   Profile = "explore"
	ProfileImplement Profile = "implement"
	ProfileValidate  Profile = "validate"
	ProfileReview    Profile = "review"
	ProfileIntegrate Profile = "integrate"
)

// PromptPolicy contains the role-specific trusted instruction appended to the
// common bounded-node policy.
type PromptPolicy struct {
	Instructions string
	ReadOnly     bool
}

// ToolPolicy names the concrete workspace tools that may be exposed. The tool
// factory fails closed when a candidate is not explicitly listed.
type ToolPolicy struct {
	Allowed    []string
	NodeScoped bool
}

// ResultPolicy constrains the structured terminal result independently of the
// model's claims.
type ResultPolicy struct {
	AllowChanges               bool
	RequireValidationOnSuccess bool
}

// WorkflowPolicy supplies trusted compilation defaults for one role.
type WorkflowPolicy struct {
	Capability      workflow.Capability
	DefaultFailure  workflow.FailureAction
	DefaultAttempts int
}

// Definition is one versioned role policy registered at application startup.
type Definition struct {
	Role          workflow.Role
	Profile       Profile
	PolicyVersion uint32
	Prompt        PromptPolicy
	Tools         ToolPolicy
	Result        ResultPolicy
	Workflow      WorkflowPolicy
}

func cloneDefinition(value Definition) Definition {
	value.Tools.Allowed = append([]string(nil), value.Tools.Allowed...)
	return value
}
