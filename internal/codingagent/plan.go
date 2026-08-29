package codingagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	maxPlanTextBytes       = 16 << 10
	maxPlanListItems       = 128
	maxPlanSteps           = 64
	maxPlanPathsPerStep    = 128
	maxPlanAcceptanceItems = 128
)

// PlanID identifies one stable Plan across immutable revisions.
type PlanID string

// PlanCompletionMode declares whether an approved Plan should start an
// implementation Run or is itself the requested deliverable.
type PlanCompletionMode string

const (
	PlanCompletionExecute     PlanCompletionMode = "execute"
	PlanCompletionDeliverable PlanCompletionMode = "deliverable"
)

// PlanScope describes the intended implementation boundary without granting writes.
type PlanScope struct {
	Included []string `json:"included"`
	Excluded []string `json:"excluded,omitempty"`
}

// PlanStep is one bounded, dependency-aware implementation step.
type PlanStep struct {
	ID         string   `json:"id"`
	Goal       string   `json:"goal"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Files      []string `json:"files,omitempty"`
	Validation []string `json:"validation"`
}

// WorkspaceRevision binds a Plan to the trusted worktree facts observed at submission.
type WorkspaceRevision struct {
	WorktreeID   WorktreeID `json:"worktree_id"`
	GitHead      string     `json:"git_head,omitempty"`
	StatusDigest string     `json:"status_digest"`
	RecordedAt   time.Time  `json:"recorded_at"`
}

// Plan is one immutable, structured implementation-plan revision.
type Plan struct {
	ID                  PlanID             `json:"id"`
	TurnID              TurnID             `json:"turn_id"`
	Version             uint64             `json:"version"`
	Goal                string             `json:"goal"`
	Scope               PlanScope          `json:"scope"`
	Findings            []string           `json:"findings"`
	Assumptions         []string           `json:"assumptions,omitempty"`
	Risks               []string           `json:"risks"`
	Steps               []PlanStep         `json:"steps"`
	AcceptanceCriteria  []string           `json:"acceptance_criteria"`
	RecommendedStrategy ExecutionStrategy  `json:"recommended_strategy"`
	WorkspaceRelevant   bool               `json:"workspace_relevant"`
	CompletionMode      PlanCompletionMode `json:"completion_mode"`
	WorkspaceRevision   WorkspaceRevision  `json:"workspace_revision"`
	Digest              string             `json:"digest"`
	CreatedAt           time.Time          `json:"created_at"`
}

// PlanSubmission is the model-proposed portion of a Plan. Trusted identities,
// version, workspace facts, digest, and timestamps are supplied by the product.
type PlanSubmission struct {
	Goal                string             `json:"goal"`
	Scope               PlanScope          `json:"scope"`
	Findings            []string           `json:"findings"`
	Assumptions         []string           `json:"assumptions,omitempty"`
	Risks               []string           `json:"risks"`
	Steps               []PlanStep         `json:"steps"`
	AcceptanceCriteria  []string           `json:"acceptance_criteria"`
	RecommendedStrategy ExecutionStrategy  `json:"recommended_strategy"`
	WorkspaceRelevant   bool               `json:"workspace_relevant"`
	CompletionMode      PlanCompletionMode `json:"completion_mode"`
}

// ApplyPlanCompatibilityDefaults upgrades a Plan decoded from the original P1
// persistence shape. It deliberately preserves Digest because Product Turns
// bind the immutable revision by that original digest.
func ApplyPlanCompatibilityDefaults(value Plan) Plan {
	if value.CompletionMode == "" {
		value.CompletionMode = PlanCompletionExecute
		value.WorkspaceRelevant = value.WorkspaceRevision != (WorkspaceRevision{})
	}
	return value
}

// ValidatePlan checks the immutable Plan repository contract and canonical digest.
func ValidatePlan(value Plan) error {
	if value.ID == "" || value.TurnID == "" || value.Version == 0 || value.CreatedAt.IsZero() {
		return errors.New("Coding plan identity, turn, version, and creation time are required")
	}
	if value.RecommendedStrategy != ExecutionSingle {
		return fmt.Errorf("Coding plan execution strategy %q is unsupported", value.RecommendedStrategy)
	}
	if err := validatePlanSubmission(PlanSubmission{
		Goal: value.Goal, Scope: value.Scope, Findings: value.Findings, Assumptions: value.Assumptions,
		Risks: value.Risks, Steps: value.Steps, AcceptanceCriteria: value.AcceptanceCriteria,
		RecommendedStrategy: value.RecommendedStrategy, WorkspaceRelevant: value.WorkspaceRelevant,
		CompletionMode: value.CompletionMode,
	}); err != nil {
		return err
	}
	if value.WorkspaceRelevant {
		if value.WorkspaceRevision.WorktreeID == "" || value.WorkspaceRevision.RecordedAt.IsZero() {
			return errors.New("Workspace-relevant Coding plan requires a worktree revision and timestamp")
		}
		if value.WorkspaceRevision.GitHead != "" && !isHexDigest(value.WorkspaceRevision.GitHead, 40, 64) {
			return errors.New("Coding plan Git HEAD is invalid")
		}
		if !isHexDigest(value.WorkspaceRevision.StatusDigest, 64, 64) {
			return errors.New("Coding plan workspace status digest is invalid")
		}
	} else if value.WorkspaceRevision != (WorkspaceRevision{}) {
		return errors.New("Workspace-independent Coding plan cannot carry a workspace revision")
	}
	digest, err := ComputePlanDigest(value)
	if err != nil {
		return err
	}
	if value.Digest != digest {
		legacyDigest, legacyErr := computeLegacyPlanDigest(value)
		if legacyErr != nil || value.CompletionMode != PlanCompletionExecute || !value.WorkspaceRelevant || value.Digest != legacyDigest {
			return errors.New("Coding plan digest does not match its canonical content")
		}
	}
	return nil
}

func validatePlanSubmission(value PlanSubmission) error {
	if err := validatePlanText("goal", value.Goal, true); err != nil {
		return err
	}
	if value.RecommendedStrategy != ExecutionSingle {
		return fmt.Errorf("Coding plan execution strategy %q is unsupported", value.RecommendedStrategy)
	}
	if value.CompletionMode != PlanCompletionExecute && value.CompletionMode != PlanCompletionDeliverable {
		return fmt.Errorf("Coding plan completion mode %q is unsupported", value.CompletionMode)
	}
	if value.CompletionMode == PlanCompletionExecute && !value.WorkspaceRelevant {
		return errors.New("An executable Coding plan must be relevant to the current workspace")
	}
	if err := validatePlanTextList("included scope", value.Scope.Included, 1, maxPlanListItems); err != nil {
		return err
	}
	if err := validatePlanTextList("excluded scope", value.Scope.Excluded, 0, maxPlanListItems); err != nil {
		return err
	}
	if err := validatePlanTextList("findings", value.Findings, 1, maxPlanListItems); err != nil {
		return err
	}
	if err := validatePlanTextList("assumptions", value.Assumptions, 0, maxPlanListItems); err != nil {
		return err
	}
	if err := validatePlanTextList("risks", value.Risks, 1, maxPlanListItems); err != nil {
		return err
	}
	if err := validatePlanTextList("acceptance criteria", value.AcceptanceCriteria, 1, maxPlanAcceptanceItems); err != nil {
		return err
	}
	if len(value.Steps) == 0 || len(value.Steps) > maxPlanSteps {
		return fmt.Errorf("Coding plan requires between 1 and %d steps", maxPlanSteps)
	}
	stepIDs := make(map[string]struct{}, len(value.Steps))
	for index, step := range value.Steps {
		if !validPlanStepID(step.ID) {
			return fmt.Errorf("Coding plan step %d has an invalid id", index)
		}
		if _, exists := stepIDs[step.ID]; exists {
			return fmt.Errorf("Coding plan step %q is duplicated", step.ID)
		}
		stepIDs[step.ID] = struct{}{}
		if err := validatePlanText(fmt.Sprintf("step %q goal", step.ID), step.Goal, true); err != nil {
			return err
		}
		if err := validatePlanTextList(fmt.Sprintf("step %q validation", step.ID), step.Validation, 1, maxPlanListItems); err != nil {
			return err
		}
		if len(step.DependsOn) > maxPlanSteps || len(step.Files) > maxPlanPathsPerStep {
			return fmt.Errorf("Coding plan step %q exceeds dependency or file limits", step.ID)
		}
		seenDependencies := make(map[string]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			if dependency == step.ID {
				return fmt.Errorf("Coding plan step %q cannot depend on itself", step.ID)
			}
			if _, exists := seenDependencies[dependency]; exists {
				return fmt.Errorf("Coding plan step %q repeats dependency %q", step.ID, dependency)
			}
			seenDependencies[dependency] = struct{}{}
		}
		seenFiles := make(map[string]struct{}, len(step.Files))
		for _, file := range step.Files {
			if !value.WorkspaceRelevant {
				return fmt.Errorf("Workspace-independent Coding plan step %q cannot declare file scope", step.ID)
			}
			normalized, err := NormalizePlanPath(file)
			if err != nil || normalized != file {
				return fmt.Errorf("Coding plan step %q contains non-canonical file scope %q", step.ID, file)
			}
			if _, exists := seenFiles[file]; exists {
				return fmt.Errorf("Coding plan step %q repeats file scope %q", step.ID, file)
			}
			seenFiles[file] = struct{}{}
		}
	}
	for _, step := range value.Steps {
		for _, dependency := range step.DependsOn {
			if _, exists := stepIDs[dependency]; !exists {
				return fmt.Errorf("Coding plan step %q has missing dependency %q", step.ID, dependency)
			}
		}
	}
	if planHasCycle(value.Steps) {
		return errors.New("Coding plan step dependencies contain a cycle")
	}
	return nil
}

// NormalizePlanPath returns a portable worktree-relative Plan scope.
func NormalizePlanPath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("Plan path must be a non-empty worktree-relative path")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, ":") {
		return "", errors.New("Plan path escapes the worktree or is not portable")
	}
	return cleaned, nil
}

// ComputePlanDigest hashes the canonical immutable Plan content without its digest field.
func ComputePlanDigest(value Plan) (string, error) {
	value.Digest = ""
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("compute Coding plan digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func computeLegacyPlanDigest(value Plan) (string, error) {
	legacy := struct {
		ID                  PlanID            `json:"id"`
		TurnID              TurnID            `json:"turn_id"`
		Version             uint64            `json:"version"`
		Goal                string            `json:"goal"`
		Scope               PlanScope         `json:"scope"`
		Findings            []string          `json:"findings"`
		Assumptions         []string          `json:"assumptions,omitempty"`
		Risks               []string          `json:"risks"`
		Steps               []PlanStep        `json:"steps"`
		AcceptanceCriteria  []string          `json:"acceptance_criteria"`
		RecommendedStrategy ExecutionStrategy `json:"recommended_strategy"`
		WorkspaceRevision   WorkspaceRevision `json:"workspace_revision"`
		Digest              string            `json:"digest"`
		CreatedAt           time.Time         `json:"created_at"`
	}{
		ID: value.ID, TurnID: value.TurnID, Version: value.Version, Goal: value.Goal,
		Scope: value.Scope, Findings: value.Findings, Assumptions: value.Assumptions, Risks: value.Risks,
		Steps: value.Steps, AcceptanceCriteria: value.AcceptanceCriteria, RecommendedStrategy: value.RecommendedStrategy,
		WorkspaceRevision: value.WorkspaceRevision, CreatedAt: value.CreatedAt,
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		return "", fmt.Errorf("compute legacy Coding plan digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validatePlanText(label, value string, required bool) error {
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return fmt.Errorf("Coding plan %s is required", label)
	}
	if value != trimmed || len(value) > maxPlanTextBytes || strings.ContainsRune(value, 0) {
		return fmt.Errorf("Coding plan %s is not normalized or exceeds its size limit", label)
	}
	return nil
}

func validatePlanTextList(label string, values []string, minimum, maximum int) error {
	if len(values) < minimum || len(values) > maximum {
		return fmt.Errorf("Coding plan %s requires between %d and %d items", label, minimum, maximum)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validatePlanText(label, value, true); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("Coding plan %s contains a duplicate item", label)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validPlanStepID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func planHasCycle(steps []PlanStep) bool {
	dependencies := make(map[string][]string, len(steps))
	for _, step := range steps {
		dependencies[step.ID] = append([]string(nil), step.DependsOn...)
		sort.Strings(dependencies[step.ID])
	}
	visiting := make(map[string]bool, len(steps))
	visited := make(map[string]bool, len(steps))
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dependency := range dependencies[id] {
			if visit(dependency) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}
	for _, step := range steps {
		if visit(step.ID) {
			return true
		}
	}
	return false
}

func isHexDigest(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
