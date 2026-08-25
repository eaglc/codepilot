package codingtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const toolResultArtifactMediaType = "application/vnd.codepilot.tool-result+json"

const (
	defaultToolResultChunkBytes = 16 << 10
	maxToolResultChunkBytes     = 48 << 10
)

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

type toolResultChunkDetails struct {
	Kind         string `json:"kind"`
	ArtifactID   string `json:"artifact_id"`
	ArtifactSize int64  `json:"artifact_size"`
	Offset       int    `json:"offset"`
	NextOffset   int    `json:"next_offset,omitempty"`
	TotalBytes   int    `json:"total_bytes"`
	Complete     bool   `json:"complete"`
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
	preview += fmt.Sprintf("[Complete result stored as %s, %d bytes. Use read_tool_result with artifact_id and artifact_size to read it.]", reference.ID, reference.Size)
	result.Content = []llm.Content{{Type: llm.ContentText, Text: preview}}
	result.Details = details
	return result
}

type readToolResultTool struct {
	artifacts codingagent.ArtifactReader
}

func (*readToolResultTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "read_tool_result",
		Description: "Read a bounded UTF-8 chunk of a complete tool result that was stored as an artifact. Use the artifact_id and artifact_size shown in the truncated tool result; continue with next_offset until complete is true.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"artifact_id":{"type":"string"},"artifact_size":{"type":"integer","minimum":1},"offset":{"type":"integer","minimum":0},"max_bytes":{"type":"integer","minimum":256,"maximum":49152}},"required":["artifact_id","artifact_size"],"additionalProperties":false}`),
	}
}

func (*readToolResultTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *readToolResultTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct {
		ArtifactID   string `json:"artifact_id"`
		ArtifactSize int64  `json:"artifact_size"`
		Offset       int    `json:"offset"`
		MaxBytes     int    `json:"max_bytes"`
	}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	arguments.ArtifactID = strings.TrimSpace(arguments.ArtifactID)
	if arguments.ArtifactID == "" || arguments.ArtifactSize <= 0 {
		return invalidResult("artifact_id and artifact_size must identify a stored tool result."), nil
	}
	if arguments.MaxBytes == 0 {
		arguments.MaxBytes = defaultToolResultChunkBytes
	}
	if arguments.MaxBytes < 256 || arguments.MaxBytes > maxToolResultChunkBytes {
		return invalidResult(fmt.Sprintf("max_bytes must be between 256 and %d.", maxToolResultChunkBytes)), nil
	}
	reference := codingagent.ArtifactRef{ID: arguments.ArtifactID, MediaType: toolResultArtifactMediaType, Size: arguments.ArtifactSize}
	artifact, err := t.artifacts.LoadArtifact(ctx, reference)
	if err != nil {
		return failedResult("The stored tool result could not be loaded or failed its integrity check."), nil
	}
	if artifact.MediaType != toolResultArtifactMediaType {
		return failedResult("The artifact is not a stored tool result."), nil
	}
	var persisted durableToolResult
	if err := json.Unmarshal(artifact.Data, &persisted); err != nil || persisted.Version != 1 || persisted.ToolName == "" || persisted.CallID == "" || persisted.Result.Validate() != nil || !textOnly(persisted.Result.Content) {
		return failedResult("The stored tool result has an invalid format."), nil
	}
	document := renderDurableToolResult(persisted)
	if arguments.Offset < 0 || arguments.Offset > len(document) || arguments.Offset < len(document) && !utf8.RuneStart(document[arguments.Offset]) {
		return invalidResult("offset must be a UTF-8 boundary within the stored tool result."), nil
	}
	end := arguments.Offset + arguments.MaxBytes
	if end > len(document) {
		end = len(document)
	}
	for end > arguments.Offset && !utf8.Valid(document[arguments.Offset:end]) {
		end--
	}
	complete := end == len(document)
	nextOffset := end
	if complete {
		nextOffset = 0
	}
	text := string(document[arguments.Offset:end])
	text += fmt.Sprintf("\n\n[Tool result bytes %d-%d of %d; complete=%t", arguments.Offset, end, len(document), complete)
	if !complete {
		text += fmt.Sprintf("; next_offset=%d", nextOffset)
	}
	text += "]"
	details, _ := json.Marshal(toolResultChunkDetails{
		Kind: "coding_tool_result_chunk_v1", ArtifactID: reference.ID, ArtifactSize: reference.Size,
		Offset: arguments.Offset, NextOffset: nextOffset, TotalBytes: len(document), Complete: complete,
	})
	return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: text}}, Details: details}, nil
}

func renderDurableToolResult(persisted durableToolResult) []byte {
	var document strings.Builder
	fmt.Fprintf(&document, "Tool: %s\nCall ID: %s\nStatus: %s\n", persisted.ToolName, persisted.CallID, persisted.Result.Status)
	if len(persisted.Result.Details) != 0 {
		fmt.Fprintf(&document, "Details: %s\n", persisted.Result.Details)
	}
	for index, content := range persisted.Result.Content {
		fmt.Fprintf(&document, "Content %d (%s):\n%s", index+1, content.Type, content.Text)
		if index+1 != len(persisted.Result.Content) {
			document.WriteByte('\n')
		}
	}
	return []byte(document.String())
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
