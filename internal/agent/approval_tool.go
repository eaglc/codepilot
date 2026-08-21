package agent

import (
	"encoding/json"
	"time"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

// approvalInterruptPayload is deliberately bounded and omits patch bodies,
// worktree roots, and fingerprints from provider-facing interrupt data.
type approvalInterruptPayload struct {
	RequestID      string             `json:"request_id"`
	SessionID      string             `json:"session_id"`
	TurnID         string             `json:"turn_id"`
	Kind           session.ActionKind `json:"kind"`
	Summary        string             `json:"summary,omitempty"`
	Files          []string           `json:"files,omitempty"`
	FilesTruncated bool               `json:"files_truncated,omitempty"`
	Command        *approvalCommand   `json:"command,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
}

// approvalCommand exposes enough structured context for a decision without
// allowing an approval interrupt to bypass normal tool-result limits.
type approvalCommand struct {
	Program   string   `json:"program"`
	Args      []string `json:"args"`
	Truncated bool     `json:"truncated,omitempty"`
	TimeoutMS int64    `json:"timeout_ms"`
}

// approvalInterruptResult preserves the request ID so a later decision can be
// matched to the exact pending action.
func approvalInterruptResult(request session.ApprovalRequest) (tool.Result, error) {
	payload := approvalInterruptPayload{
		RequestID: string(request.ID),
		SessionID: string(request.SessionID),
		TurnID:    string(request.TurnID),
		Kind:      request.Action.Kind,
		Summary:   request.Action.Summary,
		CreatedAt: request.CreatedAt,
	}
	if request.Action.Patch != nil {
		payload.Files, payload.FilesTruncated = boundedApprovalStrings(request.Action.Patch.Files, 50, 256)
	}
	if request.Action.Command != nil {
		arguments, truncated := boundedApprovalStrings(request.Action.Command.Args, 32, 256)
		payload.Command = &approvalCommand{
			Program:   request.Action.Command.Program,
			Args:      arguments,
			Truncated: truncated,
			TimeoutMS: request.Action.Command.Timeout.Milliseconds(),
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		Status:  tool.ResultInterrupted,
		Content: "The tool is waiting for user approval.",
		Interrupt: &tool.Interrupt{
			ID:      string(request.ID),
			Kind:    "approval",
			Payload: encoded,
		},
	}, nil
}

// boundedApprovalStrings caps both collection length and individual value size
// before approval context crosses the model boundary.
func boundedApprovalStrings(values []string, maximum int, maxBytes int) ([]string, bool) {
	count := min(len(values), maximum)
	bounded := make([]string, 0, count)
	truncated := len(values) > maximum
	for _, value := range values[:count] {
		shortened := truncateToolText(value, maxBytes)
		truncated = truncated || shortened != value
		bounded = append(bounded, shortened)
	}
	return bounded, truncated
}
