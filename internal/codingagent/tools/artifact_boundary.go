package codingtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const toolResultArtifactMediaType = "application/vnd.codepilot.tool-result+json"

type artifactBoundary struct {
	inner     tool.Tool
	store     codingagent.ArtifactStore
	threshold int
	preview   int
}

type resumableArtifactBoundary struct{ artifactBoundary }

func withArtifactBoundary(inner tool.Tool, store codingagent.ArtifactStore, threshold, preview int) tool.Tool {
	boundary := artifactBoundary{inner: inner, store: store, threshold: threshold, preview: preview}
	if _, ok := inner.(tool.ResumableTool); ok {
		return &resumableArtifactBoundary{artifactBoundary: boundary}
	}
	return &boundary
}

func (b *artifactBoundary) Definition() llm.ToolDefinition  { return b.inner.Definition() }
func (b *artifactBoundary) ReplayPolicy() tool.ReplayPolicy { return b.inner.ReplayPolicy() }

func (b *artifactBoundary) Execute(ctx context.Context, call tool.Call, progress tool.ProgressSink) (tool.Result, error) {
	result, err := b.inner.Execute(ctx, call, progress)
	if err != nil {
		return tool.Result{}, err
	}
	return b.externalize(ctx, call, result), nil
}

func (b *resumableArtifactBoundary) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, progress tool.ProgressSink) (tool.Result, error) {
	inner := b.inner.(tool.ResumableTool)
	result, err := inner.Resume(ctx, call, interrupt, resolution, progress)
	if err != nil {
		return tool.Result{}, err
	}
	return b.externalize(ctx, call, result), nil
}

type durableToolResult struct {
	Version  int         `json:"version"`
	ToolName string      `json:"tool_name"`
	CallID   string      `json:"call_id"`
	Result   tool.Result `json:"result"`
}

type artifactResultDetails struct {
	Kind      string                  `json:"kind"`
	Detail    string                  `json:"detail"`
	Artifact  codingagent.ArtifactRef `json:"artifact"`
	Path      string                  `json:"path,omitempty"`
	StartLine int                     `json:"start_line,omitempty"`
	EndLine   int                     `json:"end_line,omitempty"`
	Diff      *artifactDiffPreview    `json:"diff,omitempty"`
}

type artifactDiffPreview struct {
	Text  string   `json:"text"`
	Files []string `json:"files,omitempty"`
}

func (b *artifactBoundary) externalize(ctx context.Context, call tool.Call, result tool.Result) tool.Result {
	if b.store == nil || result.Status == tool.ResultInterrupted || !textOnly(result.Content) {
		return result
	}
	payload, err := json.Marshal(durableToolResult{Version: 1, ToolName: call.Name, CallID: call.ID, Result: result.Clone()})
	if err != nil || len(payload) <= b.threshold {
		return result
	}
	reference, err := b.store.SaveArtifact(ctx, codingagent.Artifact{MediaType: toolResultArtifactMediaType, Data: payload})
	if err != nil {
		return result
	}
	detail, diff := artifactPreviewDetails(result.Details, b.preview)
	path, startLine, endLine := artifactResourcePreview(result.Details)
	if detail == "" {
		detail = fmt.Sprintf("Complete %s result is stored as %s (%d bytes).", call.Name, reference.ID, reference.Size)
	}
	details, _ := json.Marshal(artifactResultDetails{
		Kind: "coding_tool_artifact_v1", Detail: detail, Artifact: reference,
		Path: path, StartLine: startLine, EndLine: endLine, Diff: diff,
	})
	preview := boundedResultText(result.Content, b.preview)
	if preview != "" {
		preview += "\n\n"
	}
	preview += fmt.Sprintf("[Complete result stored as %s, %d bytes]", reference.ID, reference.Size)
	result.Content = []llm.Content{{Type: llm.ContentText, Text: preview}}
	result.Details = details
	return result
}

func textOnly(content []llm.Content) bool {
	for _, item := range content {
		if item.Type != llm.ContentText {
			return false
		}
	}
	return true
}

func boundedResultText(content []llm.Content, limit int) string {
	var values []string
	for _, item := range content {
		if item.Type == llm.ContentText && item.Text != "" {
			values = append(values, item.Text)
		}
	}
	return boundedText(strings.Join(values, "\n"), limit)
}

func artifactPreviewDetails(raw json.RawMessage, limit int) (string, *artifactDiffPreview) {
	var value struct {
		Detail string `json:"detail"`
		Diff   *struct {
			Text  string   `json:"text"`
			Files []string `json:"files"`
		} `json:"diff"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return "", nil
	}
	detail := boundedText(value.Detail, limit)
	if value.Diff == nil || value.Diff.Text == "" {
		return detail, nil
	}
	return detail, &artifactDiffPreview{Text: boundedText(value.Diff.Text, limit), Files: append([]string(nil), value.Diff.Files...)}
}

func artifactResourcePreview(raw json.RawMessage) (string, int, int) {
	var value struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if json.Unmarshal(raw, &value) != nil || value.StartLine <= 0 || value.EndLine < value.StartLine {
		return "", 0, 0
	}
	value.Path = strings.TrimSpace(value.Path)
	if value.Path == "" || len(value.Path) > 4096 || strings.ContainsAny(value.Path, "\r\n\x00") {
		return "", 0, 0
	}
	return value.Path, value.StartLine, value.EndLine
}
