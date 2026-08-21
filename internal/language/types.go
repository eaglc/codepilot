package language

import "time"

// CheckCommand is a trusted command plan produced by a language strategy. Dir
// is worktree-relative and an empty value selects the worktree root.
type CheckCommand struct {
	ID             string
	Program        string
	Args           []string
	Dir            string
	EnvAllowlist   []string
	Timeout        time.Duration
	MaxOutputBytes int
}

// LanguageID is the stable identifier exposed by a language strategy.
type LanguageID string

const (
	// LanguageGeneric selects language-neutral behavior.
	LanguageGeneric LanguageID = "generic"
	// LanguageGo selects Go-specific prompt guidance and check plans.
	LanguageGo LanguageID = "go"
	// LanguagePython selects Python-specific prompt guidance and check plans.
	LanguagePython LanguageID = "python"
)

// CheckPlan is one immutable, model-selectable verification plan.
type CheckPlan struct {
	ID          string
	Description string
	Command     CheckCommand
}

// LanguageProfile contains prompt guidance and trusted check plans for one
// resolved worktree language.
type LanguageProfile struct {
	ID         LanguageID
	PromptHint string
	CheckPlans []CheckPlan
}
