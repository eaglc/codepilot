package roleprofile

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/eaglc/codepilot/internal/workflow"
)

var knownWorkspaceTools = map[string]bool{
	"read_file": true, "list_files": true, "search_code": true,
	"git_status": true, "git_diff": true, "git_log": true, "git_branches": true, "git_show_commit": true,
	"apply_patch": true, "create_file": true, "edit_file": true, "replace_file": true,
	"list_check_plans": true, "run_checks": true, "read_tool_result": true,
	"go_to_definition": true, "find_references": true, "diagnostics": true, "document_symbols": true,
}

var mutationTools = map[string]bool{
	"apply_patch": true, "create_file": true, "edit_file": true, "replace_file": true,
}

// Registry is an immutable, one-to-one mapping between roles and profiles.
// It deliberately has no mutation API so security policy cannot drift after
// startup validation.
type Registry struct {
	byRole           map[workflow.Role]map[uint32]Definition
	byProfile        map[Profile]map[uint32]Definition
	currentByRole    map[workflow.Role]Definition
	currentByProfile map[Profile]Definition
}

// NewRegistry validates and registers explicit role definitions.
func NewRegistry(definitions ...Definition) (*Registry, error) {
	if len(definitions) == 0 {
		return nil, errors.New("create role profile registry: at least one definition is required")
	}
	registry := &Registry{
		byRole: make(map[workflow.Role]map[uint32]Definition), byProfile: make(map[Profile]map[uint32]Definition),
		currentByRole: make(map[workflow.Role]Definition), currentByProfile: make(map[Profile]Definition),
	}
	for index := range definitions {
		definition := cloneDefinition(definitions[index])
		if err := validateDefinition(definition); err != nil {
			return nil, fmt.Errorf("create role profile registry: definition %d: %w", index, err)
		}
		versionsByRole := registry.byRole[definition.Role]
		if versionsByRole == nil {
			versionsByRole = make(map[uint32]Definition)
			registry.byRole[definition.Role] = versionsByRole
		} else if current := registry.currentByRole[definition.Role]; current.Profile != definition.Profile {
			return nil, fmt.Errorf("create role profile registry: role %q changes profile identity", definition.Role)
		}
		if _, exists := versionsByRole[definition.PolicyVersion]; exists {
			return nil, fmt.Errorf("create role profile registry: role %q policy version %d is duplicated", definition.Role, definition.PolicyVersion)
		}
		versionsByProfile := registry.byProfile[definition.Profile]
		if versionsByProfile == nil {
			versionsByProfile = make(map[uint32]Definition)
			registry.byProfile[definition.Profile] = versionsByProfile
		} else if current := registry.currentByProfile[definition.Profile]; current.Role != definition.Role {
			return nil, fmt.Errorf("create role profile registry: profile %q changes role identity", definition.Profile)
		}
		if _, exists := versionsByProfile[definition.PolicyVersion]; exists {
			return nil, fmt.Errorf("create role profile registry: profile %q policy version %d is duplicated", definition.Profile, definition.PolicyVersion)
		}
		versionsByRole[definition.PolicyVersion] = definition
		versionsByProfile[definition.PolicyVersion] = definition
		if current, exists := registry.currentByRole[definition.Role]; !exists || definition.PolicyVersion > current.PolicyVersion {
			registry.currentByRole[definition.Role] = definition
			registry.currentByProfile[definition.Profile] = definition
		}
	}
	return registry, nil
}

// NewDefaultRegistry constructs the built-in, explicitly ordered role set.
func NewDefaultRegistry() (*Registry, error) {
	return NewRegistry(Explore(), Implement(), Validate(), Review(), Integrate())
}

// ResolveRole resolves the current policy for a role.
func (r *Registry) ResolveRole(role workflow.Role) (Definition, error) {
	if r == nil {
		return Definition{}, errors.New("resolve role profile: registry is unavailable")
	}
	definition, exists := r.currentByRole[role]
	if !exists {
		return Definition{}, fmt.Errorf("resolve role profile: role %q is not registered", role)
	}
	return cloneDefinition(definition), nil
}

// ResolveProfile resolves the current policy for a capability profile.
func (r *Registry) ResolveProfile(profile Profile) (Definition, error) {
	if r == nil {
		return Definition{}, errors.New("resolve role profile: registry is unavailable")
	}
	definition, exists := r.currentByProfile[profile]
	if !exists {
		return Definition{}, fmt.Errorf("resolve role profile: profile %q is not registered", profile)
	}
	return cloneDefinition(definition), nil
}

// ResolveVersion requires an exact persisted policy version. Version zero is
// the compatibility identity for P5 data written before policy versioning and
// resolves to the initial built-in version only.
func (r *Registry) ResolveVersion(role workflow.Role, profile Profile, version uint32) (Definition, error) {
	if r == nil {
		return Definition{}, errors.New("resolve role profile: registry is unavailable")
	}
	if version == 0 {
		version = 1
	}
	versions := r.byRole[role]
	definition, exists := versions[version]
	if !exists {
		return Definition{}, fmt.Errorf("resolve role profile: %s/%s policy version %d is unavailable", role, profile, version)
	}
	if definition.Profile != profile {
		return Definition{}, fmt.Errorf("resolve role profile: role %q does not match profile %q", role, profile)
	}
	return definition, nil
}

// Definitions returns a stable defensive copy for diagnostics and tests.
func (r *Registry) Definitions() []Definition {
	if r == nil {
		return nil
	}
	values := make([]Definition, 0, len(r.currentByRole))
	for _, definition := range r.currentByRole {
		values = append(values, cloneDefinition(definition))
	}
	sort.Slice(values, func(left, right int) bool { return values[left].Role < values[right].Role })
	return values
}

func validateDefinition(value Definition) error {
	if value.Role == "" || value.Profile == "" || value.PolicyVersion == 0 {
		return errors.New("role, profile, and policy version are required")
	}
	if string(value.Role) != string(value.Profile) || value.Workflow.Capability != workflow.Capability(value.Role) {
		return errors.New("role, profile, and Workflow capability must be the same stable identity")
	}
	if strings.TrimSpace(value.Prompt.Instructions) == "" {
		return errors.New("trusted role instructions are required")
	}
	requiresReadOnly := value.Role == workflow.RoleExplore || value.Role == workflow.RoleValidate || value.Role == workflow.RoleReview
	if requiresReadOnly && !value.Prompt.ReadOnly {
		return fmt.Errorf("role %q must remain read-only", value.Role)
	}
	if value.Role == workflow.RoleValidate && !value.Result.RequireValidationOnSuccess {
		return errors.New("Validate role must require validation evidence on success")
	}
	if value.Workflow.DefaultAttempts <= 0 || value.Workflow.DefaultAttempts > 8 {
		return errors.New("default attempts must be between one and eight")
	}
	switch value.Workflow.DefaultFailure {
	case workflow.FailureRetry, workflow.FailureBlock, workflow.FailureReplan, workflow.FailureTerminate:
	default:
		return fmt.Errorf("default failure action %q is unsupported", value.Workflow.DefaultFailure)
	}
	if len(value.Tools.Allowed) == 0 || !value.Tools.NodeScoped {
		return errors.New("a role must declare a non-empty node-scoped tool allowlist")
	}
	seen := make(map[string]bool, len(value.Tools.Allowed))
	for _, name := range value.Tools.Allowed {
		if !knownWorkspaceTools[name] {
			return fmt.Errorf("workspace tool %q is unknown", name)
		}
		if seen[name] {
			return fmt.Errorf("workspace tool %q is duplicated", name)
		}
		if value.Prompt.ReadOnly && mutationTools[name] {
			return fmt.Errorf("read-only role cannot allow mutation tool %q", name)
		}
		seen[name] = true
	}
	if value.Prompt.ReadOnly && value.Result.AllowChanges {
		return errors.New("read-only role cannot allow changed-path results")
	}
	return nil
}
