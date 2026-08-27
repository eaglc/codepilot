package codingagent

import (
	"encoding/json"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

func TestPermissionGrantedRequiresExactToolActionPathAndActiveLifetime(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	grant := PermissionGrant{
		ID: "grant_test", Scope: PermissionGrantSession, ToolName: "apply_patch", Action: PermissionActionModify,
		Paths: []string{"internal/a.go", "internal/b.go"}, SourceTurnID: "turn", SourceInterruptID: "approval",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	if err := ValidatePermissionGrants([]PermissionGrant{grant}); err != nil {
		t.Fatalf("validate grant: %v", err)
	}
	if !PermissionGranted([]PermissionGrant{grant}, PermissionRequest{ToolName: "apply_patch", Action: PermissionActionModify, Paths: []string{"internal/b.go"}}, now) {
		t.Fatal("active exact-subset grant was not accepted")
	}
	for name, request := range map[string]PermissionRequest{
		"tool":   {ToolName: "run_checks", Action: PermissionActionModify, Paths: []string{"internal/b.go"}},
		"action": {ToolName: "apply_patch", Action: PermissionActionExecute, Paths: []string{"internal/b.go"}},
		"path":   {ToolName: "apply_patch", Action: PermissionActionModify, Paths: []string{"internal/c.go"}},
		"broad":  {ToolName: "apply_patch", Action: PermissionActionModify},
	} {
		if PermissionGranted([]PermissionGrant{grant}, request, now) {
			t.Fatalf("mismatched %s request was granted", name)
		}
	}
	if PermissionGranted([]PermissionGrant{grant}, PermissionRequest{ToolName: "apply_patch", Action: PermissionActionModify, Paths: []string{"internal/a.go"}}, grant.ExpiresAt) {
		t.Fatal("expired grant was accepted")
	}
	grant.RevokedAt = now
	if PermissionGranted([]PermissionGrant{grant}, PermissionRequest{ToolName: "apply_patch", Action: PermissionActionModify, Paths: []string{"internal/a.go"}}, now) {
		t.Fatal("revoked grant was accepted")
	}
}

func TestDeriveSessionGrantRejectsUIClaimsAndInvalidDurableScope(t *testing.T) {
	durable := agentsession.Snapshot{Records: []agentsession.Record{
		{Type: agentsession.RecordOperationStarted, RunID: "turn", Lane: agentsession.MainLane},
		{Type: agentsession.RecordToolStarted, RunID: "turn", Lane: agentsession.MainLane, Tool: &agentsession.ToolData{ToolCallID: "call", ToolName: "apply_patch"}},
		{Type: agentsession.RecordInterruptRequested, RunID: "turn", Lane: agentsession.MainLane, Interrupt: &agentsession.InterruptData{
			InterruptID: "approval", Kind: "approval", ToolCallID: "call",
			Payload: json.RawMessage(`{"kind":"coding_patch_approval_v1","version":1,"files":["../secret"],"digest":"digest"}`),
		}},
	}}
	request := ResumeTurnRequest{SessionID: "session", TurnID: "turn", InterruptID: "approval", Decision: ResolutionApproved, GrantScope: PermissionGrantSession}
	if _, err := deriveSessionGrant(Session{ID: "session"}, durable, request, "turn", time.Now().UTC()); err == nil {
		t.Fatal("invalid durable path created a grant")
	}
	request.InterruptID = "ui-invented-approval"
	if _, err := deriveSessionGrant(Session{ID: "session"}, durable, request, "turn", time.Now().UTC()); err == nil {
		t.Fatal("UI-invented interrupt created a grant")
	}
}

func TestDeriveSessionGrantSupportsExactSingleFileEditScopes(t *testing.T) {
	for _, toolName := range []string{"edit_file", "replace_file"} {
		t.Run(toolName, func(t *testing.T) {
			durable := agentsession.Snapshot{Records: []agentsession.Record{
				{Type: agentsession.RecordOperationStarted, RunID: "turn", Lane: agentsession.MainLane},
				{Type: agentsession.RecordToolStarted, RunID: "turn", Lane: agentsession.MainLane, Tool: &agentsession.ToolData{ToolCallID: "call", ToolName: toolName}},
				{Type: agentsession.RecordInterruptRequested, RunID: "turn", Lane: agentsession.MainLane, Interrupt: &agentsession.InterruptData{
					InterruptID: "approval", Kind: "approval", ToolCallID: "call",
					Payload: json.RawMessage(`{"kind":"coding_patch_approval_v1","version":1,"files":["internal/ui/model.go"],"digest":"digest"}`),
				}},
			}}
			request := ResumeTurnRequest{SessionID: "session", TurnID: "turn", InterruptID: "approval", Decision: ResolutionApproved, GrantScope: PermissionGrantSession}
			grant, err := deriveSessionGrant(Session{ID: "session"}, durable, request, "turn", time.Now().UTC())
			if err != nil {
				t.Fatalf("derive %s grant: %v", toolName, err)
			}
			if grant.ToolName != toolName || grant.Action != PermissionActionModify || len(grant.Paths) != 1 || grant.Paths[0] != "internal/ui/model.go" {
				t.Fatalf("%s grant = %#v", toolName, grant)
			}
		})
	}
}

func TestValidatePermissionGrantsRejectsUnboundedOrNonNormalizedAudit(t *testing.T) {
	now := time.Now().UTC()
	base := PermissionGrant{
		ID: "grant", Scope: PermissionGrantSession, ToolName: "apply_patch", Action: PermissionActionModify,
		Paths: []string{"main.go"}, SourceTurnID: "turn", SourceInterruptID: "approval", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	invalid := []PermissionGrant{base, base}
	if err := ValidatePermissionGrants(invalid); err == nil {
		t.Fatal("duplicate audit ids were accepted")
	}
	base.Paths = []string{"../secret"}
	if err := ValidatePermissionGrants([]PermissionGrant{base}); err == nil {
		t.Fatal("traversal grant path was accepted")
	}
	base.Paths = []string{"main.go"}
	base.ExpiresAt = now.Add(25 * time.Hour)
	if err := ValidatePermissionGrants([]PermissionGrant{base}); err == nil {
		t.Fatal("unbounded grant lifetime was accepted")
	}
}

func TestDeriveSessionGrantUsesDurableLanguageServerScope(t *testing.T) {
	durable := agentsession.Snapshot{Records: []agentsession.Record{
		{Type: agentsession.RecordOperationStarted, RunID: "turn", Lane: agentsession.MainLane},
		{Type: agentsession.RecordToolStarted, RunID: "turn", Lane: agentsession.MainLane, Tool: &agentsession.ToolData{ToolCallID: "call", ToolName: "find_definition"}},
		{Type: agentsession.RecordInterruptRequested, RunID: "turn", Lane: agentsession.MainLane, Interrupt: &agentsession.InterruptData{
			InterruptID: "approval", Kind: "approval", ToolCallID: "call",
			Payload: json.RawMessage(`{"kind":"coding_lsp_start_approval_v1","version":1,"grant_tool_name":"language_server","requested_tool":"find_definition","language":"go","program":"gopls","arguments":["serve"],"digest":"digest"}`),
		}},
	}}
	request := ResumeTurnRequest{SessionID: "session", TurnID: "turn", InterruptID: "approval", Decision: ResolutionApproved, GrantScope: PermissionGrantSession}
	grant, err := deriveSessionGrant(Session{ID: "session"}, durable, request, "turn", time.Now().UTC())
	if err != nil {
		t.Fatalf("derive language-server grant: %v", err)
	}
	if grant.ToolName != "language_server" || grant.Action != PermissionStartLanguageServerAction("go") || len(grant.Paths) != 0 {
		t.Fatalf("language-server grant = %#v", grant)
	}
}
