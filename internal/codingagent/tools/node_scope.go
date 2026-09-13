package codingtools

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type nodeScopeTool struct {
	inner      tool.Tool
	readScope  []string
	writeScope []string
}

func withNodeScopeBoundary(inner tool.Tool, readScope []string, writeScope []string) tool.Tool {
	boundary := &nodeScopeTool{inner: inner, readScope: append([]string(nil), readScope...), writeScope: append([]string(nil), writeScope...)}
	if resumable, ok := inner.(tool.ResumableTool); ok {
		return &nodeScopeResumableTool{nodeScopeTool: boundary, resumable: resumable}
	}
	return boundary
}

func (t *nodeScopeTool) Definition() llm.ToolDefinition  { return t.inner.Definition() }
func (t *nodeScopeTool) ReplayPolicy() tool.ReplayPolicy { return t.inner.ReplayPolicy() }

func (t *nodeScopeTool) Execute(ctx context.Context, call tool.Call, progress tool.ProgressSink) (tool.Result, error) {
	if denied := t.denied(call); denied != "" {
		return deniedResult(denied), nil
	}
	return t.inner.Execute(ctx, call, progress)
}

type nodeScopeResumableTool struct {
	*nodeScopeTool
	resumable tool.ResumableTool
}

func (t *nodeScopeResumableTool) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, progress tool.ProgressSink) (tool.Result, error) {
	if denied := t.denied(call); denied != "" {
		return deniedResult(denied), nil
	}
	return t.resumable.Resume(ctx, call, interrupt, resolution, progress)
}

func (t *nodeScopeTool) denied(call tool.Call) string {
	var files []string
	switch call.Name {
	case "read_file", "list_files", "search_code", "git_diff":
		var arguments struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(call.Arguments, &arguments) != nil {
			return "The Workflow node read arguments are invalid."
		}
		if strings.TrimSpace(arguments.Path) == "" {
			return "A Workflow node read must name a path inside its declared read scope."
		}
		if !withinAnyNodeScope(arguments.Path, t.readScope) {
			return "The requested read is outside this Workflow node's declared file scope."
		}
		return ""
	case "create_file", "edit_file", "replace_file":
		var arguments struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(call.Arguments, &arguments) != nil {
			return "The Workflow node write arguments are invalid."
		}
		files = []string{arguments.Path}
	case "apply_patch":
		var arguments patchArguments
		if json.Unmarshal(call.Arguments, &arguments) != nil {
			return "The Workflow node patch arguments are invalid."
		}
		parsed, err := patchFiles(arguments.Patch)
		if err != nil {
			return "The Workflow node patch scope could not be verified."
		}
		files = parsed
	default:
		return ""
	}
	if len(t.writeScope) == 0 {
		return "This Workflow node has no write scope."
	}
	for _, file := range files {
		if !withinAnyNodeScope(file, t.writeScope) {
			return "The requested write is outside this Workflow node's approved file scope."
		}
	}
	return ""
}

func withinAnyNodeScope(value string, scopes []string) bool {
	value = path.Clean(strings.TrimSpace(strings.ReplaceAll(value, "\\", "/")))
	if value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return false
	}
	for _, scope := range scopes {
		scope = path.Clean(strings.TrimSpace(strings.ReplaceAll(scope, "\\", "/")))
		if value == scope || strings.HasPrefix(value, scope+"/") {
			return true
		}
	}
	return false
}
