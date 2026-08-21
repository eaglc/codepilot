package lsp

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/workspace"
)

type document struct {
	absolute string
	relative string
	uri      string
	text     string
}

func readDocument(root string, relativePath string, maxBytes int) (document, error) {
	_, relative, err := workspace.ResolveSafeExistingFile(root, relativePath)
	if err != nil {
		return document{}, err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return document{}, documentError(session.ErrWorkspaceUnavailable, "The worktree could not be opened for code navigation.", err)
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(filepath.FromSlash(relative))
	if err != nil {
		return document{}, documentError(session.ErrWorkspaceUnavailable, "The source document could not be opened for code navigation.", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return document{}, documentError(session.ErrWorkspaceUnavailable, "The source document could not be inspected for code navigation.", err)
	}
	if info.Size() > int64(maxBytes) {
		return document{}, documentError(session.ErrInvalidInput, "The source document exceeds the code-navigation size limit.", nil)
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return document{}, documentError(session.ErrWorkspaceUnavailable, "The source document could not be read for code navigation.", err)
	}
	if len(content) > maxBytes {
		return document{}, documentError(session.ErrInvalidInput, "The source document exceeds the code-navigation size limit.", nil)
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return document{}, documentError(session.ErrInvalidInput, "Binary files cannot be used for code navigation.", nil)
	}
	absolute := filepath.Join(root, filepath.FromSlash(relative))
	return document{absolute: absolute, relative: relative, uri: fileURI(absolute), text: string(content)}, nil
}

func documentError(code session.ErrorCode, message string, cause error) error {
	return &session.AppError{Code: code, Operation: "lsp.open_document", UserMessage: message, Cause: cause, Retryable: code == session.ErrWorkspaceUnavailable}
}

func fileURI(absolute string) string {
	pathValue := filepath.ToSlash(filepath.Clean(absolute))
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(pathValue, "/") {
		pathValue = "/" + pathValue
	}
	return (&url.URL{Scheme: "file", Path: pathValue}).String()
}

func pathFromFileURI(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("decode LSP location: URI is not a local file")
	}
	pathValue := parsed.Path
	if parsed.Host != "" {
		pathValue = "//" + parsed.Host + pathValue
	}
	if runtime.GOOS == "windows" && len(pathValue) >= 3 && pathValue[0] == '/' && pathValue[2] == ':' {
		pathValue = pathValue[1:]
	}
	return filepath.Clean(filepath.FromSlash(pathValue)), nil
}

func safeLocation(root string, value protocolLocation) (agent.Location, bool) {
	absolute, err := pathFromFileURI(value.URI)
	if err != nil {
		return agent.Location{}, false
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return agent.Location{}, false
	}
	resolved, normalized, err := workspace.ResolveSafeExistingFile(root, filepath.ToSlash(relative))
	if err != nil || !samePath(resolved, absolute) || !validProtocolRange(value.Range) {
		return agent.Location{}, false
	}
	return agent.Location{
		Path: normalized,
		Range: agent.CodeRange{
			Start: agent.CodePosition{Line: value.Range.Start.Line + 1, Column: value.Range.Start.Character + 1},
			End:   agent.CodePosition{Line: value.Range.End.Line + 1, Column: value.Range.End.Character + 1},
		},
	}, true
}

func validProtocolRange(value protocolRange) bool {
	if value.Start.Line < 0 || value.Start.Character < 0 || value.End.Line < 0 || value.End.Character < 0 {
		return false
	}
	return value.End.Line > value.Start.Line || value.End.Line == value.Start.Line && value.End.Character >= value.Start.Character
}

func samePath(left string, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
