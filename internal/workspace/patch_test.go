package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/approval"
	"github.com/eaglc/codepilot/internal/session"
)

func TestServiceApplyPatchAutoEditRecordsHashes(t *testing.T) {
	fixture := newGitFixture(t)
	service := newPatchTestService(t, approval.NewService())
	request := patchRequest(fixture.root, session.PermissionAutoEdit, answerPatch())

	result, err := service.ApplyPatch(context.Background(), request)
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	if !result.Applied || result.Denied || result.PatchRecord.ID == "" || result.PatchRecord.AppliedAt.IsZero() {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	if result.ProposedDiff.Kind != session.DiffProposed || result.ProposedDiff.Text != request.Patch || len(result.ProposedDiff.Files) != 1 {
		t.Fatalf("unexpected proposed diff: %#v", result.ProposedDiff)
	}
	if len(result.PatchRecord.Files) != 1 {
		t.Fatalf("unexpected patch files: %#v", result.PatchRecord.Files)
	}
	file := result.PatchRecord.Files[0]
	if file.Path != "main.go" || len(file.BeforeHash) != 64 || len(file.AfterHash) != 64 || file.BeforeHash == file.AfterHash {
		t.Fatalf("unexpected patch hashes: %#v", file)
	}
	content, err := os.ReadFile(filepath.Join(fixture.root, "main.go"))
	if err != nil || !strings.Contains(string(content), "return 42") {
		t.Fatalf("patched content = %q, err=%v", content, err)
	}
}

func TestServiceApplyPatchReadOnlyDeniesWithoutWriting(t *testing.T) {
	fixture := newGitFixture(t)
	service := newPatchTestService(t, approval.NewService())
	request := patchRequest(fixture.root, session.PermissionReadOnly, answerPatch())

	result, err := service.ApplyPatch(context.Background(), request)
	if err != nil {
		t.Fatalf("deny patch: %v", err)
	}
	if !result.Denied || result.Applied || result.Reason == "" {
		t.Fatalf("unexpected denial: %#v", result)
	}
	assertMainAnswer(t, fixture.root, "return 41")
}

func TestServiceApplyPatchApprovalDetectsDrift(t *testing.T) {
	fixture := newGitFixture(t)
	authorizer := approval.NewService()
	service := newPatchTestService(t, authorizer)
	request := patchRequest(fixture.root, session.PermissionAsk, answerPatch())

	result, err := service.ApplyPatch(context.Background(), request)
	var approvalRequired *session.ApprovalRequiredError
	if !errors.As(err, &approvalRequired) || result.ProposedDiff.Text != request.Patch {
		t.Fatalf("approval prompt: result=%#v err=%v", result, err)
	}
	approvalRequest := approvalRequired.Request
	if err := authorizer.Resolve(context.Background(), session.ApprovalResolution{
		RequestID: approvalRequest.ID,
		SessionID: approvalRequest.SessionID,
		TurnID:    approvalRequest.TurnID,
		Decision:  session.ApprovalDecision{Kind: session.ApprovalAllowOnce},
	}); err != nil {
		t.Fatalf("resolve approval: %v", err)
	}
	if _, err := authorizer.WaitDecision(context.Background(), approvalRequest.ID); err != nil {
		t.Fatalf("wait approval: %v", err)
	}
	pathValue := filepath.Join(fixture.root, "main.go")
	content, err := os.ReadFile(pathValue)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if err := os.WriteFile(pathValue, append(content, []byte("// external change\n")...), 0o600); err != nil {
		t.Fatalf("write external change: %v", err)
	}

	result, err = service.ApplyPatch(context.Background(), request)
	if errorCode(err) != session.ErrConflict || result.Applied {
		t.Fatalf("drift result=%#v err=%v", result, err)
	}
	assertMainAnswer(t, fixture.root, "return 41")
}

func TestServiceApplyPatchRejectsUnsafeTargetsBeforeAuthorization(t *testing.T) {
	fixture := newGitFixture(t)
	authorizer := &recordingPatchAuthorizer{}
	service := newPatchTestService(t, authorizer)
	tests := []struct {
		name  string
		patch string
	}{
		{
			name:  "credential file",
			patch: "diff --git a/.env b/.env\n--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-API_KEY=initial-secret\n+API_KEY=changed\n",
		},
		{
			name:  "codex directory",
			patch: "diff --git a/.codex/config b/.codex/config\nnew file mode 100644\n--- /dev/null\n+++ b/.codex/config\n@@ -0,0 +1 @@\n+unsafe\n",
		},
		{
			name:  "symbolic link",
			patch: "diff --git a/link b/link\nnew file mode 120000\n--- /dev/null\n+++ b/link\n@@ -0,0 +1 @@\n+../outside\n\\ No newline at end of file\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.ApplyPatch(context.Background(), patchRequest(fixture.root, session.PermissionAutoEdit, test.patch))
			if errorCode(err) != session.ErrPermissionDenied && errorCode(err) != session.ErrInvalidInput {
				t.Fatalf("unsafe patch error = %v", err)
			}
		})
	}
	if authorizer.calls != 0 {
		t.Fatalf("unsafe patches reached authorization %d times", authorizer.calls)
	}
}

func TestServiceApplyPatchCheckPreventsPartialWrite(t *testing.T) {
	fixture := newGitFixture(t)
	service := newPatchTestService(t, approval.NewService())
	patch := answerPatch() + "diff --git a/src/util.go b/src/util.go\n--- a/src/util.go\n+++ b/src/util.go\n@@ -1,3 +1,3 @@\n package src\n \n-func missing() string { return \"answer\" }\n+func missing() string { return \"changed\" }\n"

	_, err := service.ApplyPatch(context.Background(), patchRequest(fixture.root, session.PermissionAutoEdit, patch))
	if errorCode(err) != session.ErrInvalidInput {
		t.Fatalf("invalid multi-file patch error = %v", err)
	}
	assertMainAnswer(t, fixture.root, "return 41")
	content, readErr := os.ReadFile(filepath.Join(fixture.root, "src", "util.go"))
	if readErr != nil || strings.Contains(string(content), "changed") {
		t.Fatalf("second file changed after failed check: %q err=%v", content, readErr)
	}
}

type recordingPatchAuthorizer struct {
	calls int
}

func (a *recordingPatchAuthorizer) Authorize(context.Context, session.PermissionMode, session.Action) (session.Authorization, error) {
	a.calls++
	return session.Authorization{Outcome: session.AuthorizationAllow}, nil
}

func newPatchTestService(t *testing.T, authorizer ActionAuthorizer) *Service {
	t.Helper()
	service, err := NewService(Dependencies{Authorizer: authorizer, Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("create patch service: %v", err)
	}
	return service
}

func patchRequest(root string, mode session.PermissionMode, patch string) agent.ApplyPatchRequest {
	return agent.ApplyPatchRequest{
		WorktreeRoot:   root,
		SessionID:      "session_patch",
		TurnID:         "turn_patch",
		PermissionMode: mode,
		Patch:          patch,
		Intent:         "Fix the answer.",
	}
}

func answerPatch() string {
	return "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -3,3 +3,3 @@\n func answer() int {\n-\treturn 41\n+\treturn 42\n }\n"
}

func assertMainAnswer(t *testing.T, root string, expected string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(content), expected) {
		t.Fatalf("main.go = %q, want %q", content, expected)
	}
}
