package prompt

import (
	"context"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
)

func TestBuilderLoadsRoleInstructionsFromRegistry(t *testing.T) {
	root := t.TempDir()
	roles, err := roleprofile.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range roles.Definitions() {
		value, buildErr := NewBuilderWithRegistry(roles).BuildSystemPrompt(context.Background(), codingagent.PromptScope{
			Profile: codingagent.CapabilityProfile(definition.Profile), WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
			ToolNames: []string{"submit_agent_task_result"},
		})
		if buildErr != nil {
			t.Fatalf("%s prompt: %v", definition.Role, buildErr)
		}
		if !strings.Contains(value, definition.Prompt.Instructions) || !strings.Contains(value, "independent child Agent") {
			t.Fatalf("%s prompt did not load its registered policy: %s", definition.Role, value)
		}
		if strings.Contains(value, "This node is read-only") != definition.Prompt.ReadOnly {
			t.Fatalf("%s read-only prompt mismatch: %s", definition.Role, value)
		}
	}
}

func TestBuilderUsesInjectedRoleDefinition(t *testing.T) {
	v1 := roleprofile.Explore()
	v1.Prompt.Instructions = "CUSTOM_REGISTERED_EXPLORE_POLICY_V1"
	v2 := roleprofile.Explore()
	v2.PolicyVersion = 2
	v2.Prompt.Instructions = "CUSTOM_REGISTERED_EXPLORE_POLICY_V2"
	roles, err := roleprofile.NewRegistry(v1, v2)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		version uint32
		want    string
		reject  string
	}{{1, v1.Prompt.Instructions, v2.Prompt.Instructions}, {2, v2.Prompt.Instructions, v1.Prompt.Instructions}} {
		value, buildErr := NewBuilderWithRegistry(roles).BuildSystemPrompt(context.Background(), codingagent.PromptScope{
			Profile: codingagent.CapabilityExplore, PolicyVersion: test.version, WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: t.TempDir(),
		})
		if buildErr != nil || !strings.Contains(value, test.want) || strings.Contains(value, test.reject) {
			t.Fatalf("custom role prompt v%d = %q, %v", test.version, value, buildErr)
		}
	}
}
