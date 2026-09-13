package roleprofile

import (
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/workflow"
)

func TestDefaultRegistryContainsFiveImmutableRolePolicies(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 5 {
		t.Fatalf("definitions = %#v", definitions)
	}
	for _, definition := range definitions {
		if definition.PolicyVersion != 1 || definition.Role == "" || string(definition.Profile) != string(definition.Role) || definition.Workflow.Capability != workflow.Capability(definition.Role) || strings.TrimSpace(definition.Prompt.Instructions) == "" {
			t.Fatalf("invalid built-in definition = %#v", definition)
		}
	}
	explore, err := registry.ResolveRole(workflow.RoleExplore)
	if err != nil {
		t.Fatal(err)
	}
	explore.Tools.Allowed[0] = "edit_file"
	again, _ := registry.ResolveRole(workflow.RoleExplore)
	if again.Tools.Allowed[0] == "edit_file" {
		t.Fatal("registry definition was mutated through a returned slice")
	}
}

func TestRegistryFailsClosedForDuplicateUnknownAndWritableReadOnlyPolicy(t *testing.T) {
	if _, err := NewRegistry(Explore(), Explore()); err == nil {
		t.Fatal("duplicate role was accepted")
	}
	unknown := Explore()
	unknown.Tools.Allowed = append(unknown.Tools.Allowed, "invented_tool")
	if _, err := NewRegistry(unknown); err == nil {
		t.Fatal("unknown tool was accepted")
	}
	writable := Explore()
	writable.Tools.Allowed = append(writable.Tools.Allowed, "edit_file")
	if _, err := NewRegistry(writable); err == nil {
		t.Fatal("read-only role accepted a mutation tool")
	}
	notReadOnly := Review()
	notReadOnly.Prompt.ReadOnly = false
	if _, err := NewRegistry(notReadOnly); err == nil {
		t.Fatal("Review role accepted a non-read-only policy")
	}
}

func TestRegistryRequiresExactPersistedPolicyVersion(t *testing.T) {
	v1 := Validate()
	v2 := Validate()
	v2.PolicyVersion = 2
	v2.Prompt.Instructions = "Version two validation policy."
	registry, err := NewRegistry(v1, v2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveVersion(workflow.RoleValidate, ProfileValidate, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveVersion(workflow.RoleValidate, ProfileValidate, 0); err != nil {
		t.Fatalf("legacy P5 policy version: %v", err)
	}
	current, err := registry.ResolveRole(workflow.RoleValidate)
	if err != nil || current.PolicyVersion != 2 {
		t.Fatalf("current policy = %#v, %v", current, err)
	}
	if _, err := registry.ResolveVersion(workflow.RoleValidate, ProfileValidate, 3); err == nil {
		t.Fatal("unknown policy version was accepted")
	}
	if _, err := registry.ResolveVersion(workflow.RoleValidate, ProfileReview, 1); err == nil {
		t.Fatal("mismatched role and profile were accepted")
	}
}
