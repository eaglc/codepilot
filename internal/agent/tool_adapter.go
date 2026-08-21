package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

// defaultToolResultLimit is a defensive fallback; normal turns supply a
// validated limit through TurnScope.
const defaultToolResultLimit = 64 << 10

// decodeToolArguments enforces the closed JSON schemas at runtime, including
// rejection of unknown fields and trailing values.
func decodeToolArguments(arguments json.RawMessage, destination any) *tool.Result {
	if len(bytes.TrimSpace(arguments)) == 0 || bytes.Equal(bytes.TrimSpace(arguments), []byte("null")) {
		result := invalidToolResult("Tool arguments must be a JSON object.")
		return &result
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		result := invalidToolResult("Tool arguments do not match the declared schema.")
		return &result
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		result := invalidToolResult("Tool arguments must contain exactly one JSON object.")
		return &result
	}
	return nil
}

// invokeRegisteredTool dispatches only through the registry built for the
// current turn, preventing access to tools that were not explicitly bound.
func invokeRegisteredTool(ctx context.Context, registry *tool.Registry, name string, arguments json.RawMessage) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{Status: tool.ResultCancelled, Content: "The tool invocation was cancelled."}, err
	}
	if registry == nil {
		return tool.Result{}, errors.New("invoke tool: registry is nil")
	}
	registered, exists := registry.Lookup(name)
	if !exists {
		return invalidToolResult("The requested tool is not registered for this turn."), nil
	}
	return registered.Invoke(ctx, arguments)
}

func completedJSONResult(value any, limit int) tool.Result {
	return jsonResult(tool.ResultCompleted, value, limit)
}

// jsonResult applies the shared output budget. Oversized JSON is returned as
// bounded text without Data because truncation would make Data invalid JSON.
func jsonResult(status tool.ResultStatus, value any, limit int) tool.Result {
	encoded, err := json.Marshal(value)
	if err != nil {
		return tool.Result{Status: tool.ResultFailed, Content: "The tool result could not be encoded safely."}
	}
	limit = normalizedToolResultLimit(limit)
	if len(encoded) <= limit {
		return tool.Result{Status: status, Content: string(encoded), Data: encoded}
	}
	return tool.Result{
		Status:  status,
		Content: truncateToolText(string(encoded), limit),
	}
}

// normalizeToolError converts domain failures into provider-neutral statuses
// without exposing arbitrary internal causes to the model.
func normalizeToolError(ctx context.Context, err error) (tool.Result, error) {
	if err == nil {
		return tool.Result{}, nil
	}
	if contextError := ctx.Err(); contextError != nil {
		return tool.Result{Status: tool.ResultCancelled, Content: "The tool invocation was cancelled."}, contextError
	}
	var approvalRequired *session.ApprovalRequiredError
	if errors.As(err, &approvalRequired) {
		return approvalInterruptResult(approvalRequired.Request)
	}
	var appError *session.AppError
	if errors.As(err, &appError) {
		switch appError.Code {
		case session.ErrInvalidInput, session.ErrNotFound, session.ErrPermissionDenied:
			return tool.Result{Status: tool.ResultInvalid, Content: appError.Error()}, nil
		case session.ErrCancelled:
			return tool.Result{Status: tool.ResultCancelled, Content: appError.Error()}, nil
		default:
			return tool.Result{Status: tool.ResultFailed, Content: appError.Error()}, nil
		}
	}
	return tool.Result{Status: tool.ResultFailed, Content: "The tool failed because a workspace capability was unavailable."}, nil
}

func invalidToolResult(message string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: message}
}

func deniedToolResult(reason string, value any, limit int) tool.Result {
	if strings.TrimSpace(reason) == "" {
		reason = "The current permission policy denied this action."
	}
	if value == nil {
		return tool.Result{Status: tool.ResultDenied, Content: reason}
	}
	result := jsonResult(tool.ResultDenied, value, limit)
	if result.Data == nil {
		result.Content = truncateToolText(reason+" "+result.Content, normalizedToolResultLimit(limit))
	}
	return result
}

func normalizedToolResultLimit(limit int) int {
	if limit <= 0 {
		return defaultToolResultLimit
	}
	return limit
}

// truncateToolText preserves valid UTF-8 while enforcing the byte budget.
func truncateToolText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return strings.Repeat(".", limit)
	}
	value = value[:limit-3]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value + "..."
}

func definition(name string, description string, schema string) tool.Definition {
	return tool.Definition{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}

func invalidArgument(message string) (tool.Result, error) {
	return invalidToolResult(message), nil
}

func ensureString(value string, field string, maximum int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds its size limit", field)
	}
	return nil
}
