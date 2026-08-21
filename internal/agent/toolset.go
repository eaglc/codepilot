package agent

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

type toolsetDependencies struct {
	Workspaces WorkspaceTools
	State      *turnToolState
	CodeIntel  CodeNavigator
}

// turnToolState owns mutable evidence for exactly one coding turn. Sharing it
// across turns would leak proposed diffs, patch hashes, and check outcomes.
type turnToolState struct {
	mu           sync.RWMutex
	proposed     session.DiffResult
	hasProposed  bool
	patches      []session.PatchRecord
	checkSummary session.CheckSummary
}

func newTurnToolState() *turnToolState {
	return &turnToolState{checkSummary: session.CheckSummary{Outcome: session.CheckNotRun}}
}

func (s *turnToolState) recordProposed(value session.DiffResult) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proposed = cloneDiffResult(value)
	s.hasProposed = true
}

func (s *turnToolState) proposedDiff() (session.DiffResult, bool) {
	if s == nil {
		return session.DiffResult{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneDiffResult(s.proposed), s.hasProposed
}

func (s *turnToolState) recordPatch(value session.PatchRecord) {
	if s == nil || value.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.patches {
		if existing.ID == value.ID {
			return
		}
	}
	s.patches = append(s.patches, clonePatchRecord(value))
}

func (s *turnToolState) patchSnapshot() ([]string, map[string]string) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	hashes := make(map[string]string)
	for _, patch := range s.patches {
		for _, file := range patch.Files {
			hashes[file.Path] = file.AfterHash
		}
	}
	files := make([]string, 0, len(hashes))
	for pathValue := range hashes {
		files = append(files, pathValue)
	}
	sort.Strings(files)
	return files, hashes
}

// recordCheck keeps only the latest check because TurnResult reports the final
// verification state, not a history of intermediate attempts.
func (s *turnToolState) recordCheck(value RunChecksResult) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkSummary = session.CheckSummary{Outcome: value.Outcome, Summary: value.Summary, Truncated: value.Truncated}
}

func (s *turnToolState) snapshot() ([]session.PatchRecord, session.CheckSummary) {
	if s == nil {
		return nil, session.CheckSummary{Outcome: session.CheckNotRun}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	patches := make([]session.PatchRecord, len(s.patches))
	for index, value := range s.patches {
		patches[index] = clonePatchRecord(value)
	}
	return patches, s.checkSummary
}

// buildToolRegistry captures trusted turn facts in each tool so the model can
// select operations but cannot forge the worktree, session, turn, or policy.
func buildToolRegistry(scope session.TurnScope, language LanguageProfile, deps toolsetDependencies) (*tool.Registry, error) {
	if err := validateToolScope(scope); err != nil {
		return nil, err
	}
	if isNilWorkspaceTools(deps.Workspaces) {
		return nil, errors.New("build tool registry: workspace tools are required")
	}
	plans, err := cloneCheckPlans(language.CheckPlans)
	if err != nil {
		return nil, err
	}
	state := deps.State
	if state == nil {
		state = newTurnToolState()
	}
	runChecks, err := newRunChecksTool(scope, deps.Workspaces, state, plans)
	if err != nil {
		return nil, fmt.Errorf("build tool registry: encode run_checks schema: %w", err)
	}
	registry := tool.NewRegistry()
	tools := []tool.Tool{
		&listFilesTool{scope: scope, workspaces: deps.Workspaces},
		&searchCodeTool{scope: scope, workspaces: deps.Workspaces},
		&readFileTool{scope: scope, workspaces: deps.Workspaces},
		&gitStatusTool{scope: scope, workspaces: deps.Workspaces},
		&gitDiffTool{scope: scope, workspaces: deps.Workspaces, state: state},
		&applyPatchTool{scope: scope, workspaces: deps.Workspaces, state: state},
		runChecks,
	}
	if !isNilCodeNavigator(deps.CodeIntel) && (language.ID == LanguageGo || language.ID == LanguagePython) {
		navigationScope := NavigationScope{
			SessionID:      scope.SessionID,
			TurnID:         scope.TurnID,
			WorktreeID:     scope.WorktreeID,
			WorktreeRoot:   scope.WorktreeRoot,
			PermissionMode: scope.PermissionMode,
			Language:       language.ID,
		}
		tools = append(tools,
			&definitionTool{scope: scope, navigationScope: navigationScope, navigator: deps.CodeIntel},
			&referencesTool{scope: scope, navigationScope: navigationScope, navigator: deps.CodeIntel},
			&symbolsTool{scope: scope, navigationScope: navigationScope, navigator: deps.CodeIntel},
			&diagnosticsTool{scope: scope, navigationScope: navigationScope, navigator: deps.CodeIntel},
		)
	}
	for _, value := range tools {
		if err := registry.Register(value); err != nil {
			return nil, fmt.Errorf("build tool registry: %w", err)
		}
	}
	return registry, nil
}

func validateToolScope(scope session.TurnScope) error {
	if scope.SessionID == "" || scope.TurnID == "" || strings.TrimSpace(scope.WorktreeRoot) == "" {
		return errors.New("build tool registry: turn scope is incomplete")
	}
	if scope.Limits.ToolResultMaxBytes <= 0 || scope.Limits.CommandTimeout <= 0 || scope.Limits.CommandOutputMaxBytes <= 0 {
		return errors.New("build tool registry: turn limits are invalid")
	}
	switch scope.PermissionMode {
	case session.PermissionReadOnly, session.PermissionAsk, session.PermissionAutoEdit:
		return nil
	default:
		return errors.New("build tool registry: permission mode is invalid")
	}
}

// cloneCheckPlans freezes the trusted commands before their IDs are exposed in
// the run_checks schema.
func cloneCheckPlans(values []CheckPlan) (map[string]CheckPlan, error) {
	plans := make(map[string]CheckPlan, len(values))
	for _, value := range values {
		if !validCheckPlanID(value.ID) || strings.TrimSpace(value.Description) == "" || len([]rune(value.Description)) > 500 || value.Command.ID != value.ID || strings.TrimSpace(value.Command.Program) == "" {
			return nil, fmt.Errorf("build tool registry: check plan %q is invalid", value.ID)
		}
		if _, exists := plans[value.ID]; exists {
			return nil, fmt.Errorf("build tool registry: duplicate check plan %q", value.ID)
		}
		value.Command.Args = append([]string(nil), value.Command.Args...)
		value.Command.EnvAllowlist = append([]string(nil), value.Command.EnvAllowlist...)
		plans[value.ID] = value
	}
	return plans, nil
}

func validCheckPlanID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func isNilWorkspaceTools(value WorkspaceTools) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func isNilCodeNavigator(value CodeNavigator) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func cloneDiffResult(value session.DiffResult) session.DiffResult {
	value.Files = append([]session.DiffFile(nil), value.Files...)
	return value
}

func clonePatchRecord(value session.PatchRecord) session.PatchRecord {
	value.Files = append([]session.PatchedFile(nil), value.Files...)
	return value
}
