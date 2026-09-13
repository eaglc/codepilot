package codingtools

import (
	"context"
	"testing"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
)

func TestFactoryLoadsExactRoleToolAllowlists(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		profile codingagent.CapabilityProfile
		count   int
	}{
		{codingagent.CapabilityExplore, 8},
		{codingagent.CapabilityValidate, 10},
		{codingagent.CapabilityReview, 8},
		{codingagent.CapabilityImplement, 14},
		{codingagent.CapabilityIntegrate, 14},
	}
	for _, test := range tests {
		registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
			Profile: test.profile, SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
			ReadScope: []string{"internal"}, WriteScope: []string{"internal"},
		})
		if err != nil {
			t.Fatalf("%s tools: %v", test.profile, err)
		}
		if got := len(registry.Definitions()); got != test.count {
			t.Fatalf("%s tool count = %d, want %d: %#v", test.profile, got, test.count, registry.Definitions())
		}
		for _, name := range []string{"apply_patch", "create_file", "edit_file", "replace_file"} {
			_, exposed := registry.Lookup(name)
			readOnly := test.profile == codingagent.CapabilityExplore || test.profile == codingagent.CapabilityValidate || test.profile == codingagent.CapabilityReview
			if exposed == readOnly {
				t.Fatalf("%s mutation tool %q exposure = %v", test.profile, name, exposed)
			}
		}
	}
}

func TestFactoryUsesInjectedFailClosedRoleAllowlist(t *testing.T) {
	v1 := roleprofile.Explore()
	v1.Tools.Allowed = []string{"read_file"}
	v2 := roleprofile.Explore()
	v2.PolicyVersion = 2
	v2.Tools.Allowed = []string{"list_files"}
	roles, err := roleprofile.NewRegistry(v1, v2)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		version uint32
		name    string
	}{{1, "read_file"}, {2, "list_files"}} {
		registry, createErr := NewFactory(Options{Roles: roles}).CreateTools(context.Background(), codingagent.ToolScope{
			Profile: codingagent.CapabilityExplore, PolicyVersion: test.version, SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: t.TempDir(), ReadScope: []string{"internal"},
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		definitions := registry.Definitions()
		if len(definitions) != 1 || definitions[0].Name != test.name {
			t.Fatalf("custom role tools v%d = %#v", test.version, definitions)
		}
	}
}
