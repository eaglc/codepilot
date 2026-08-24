package lsp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent/language"
)

type document struct {
	path       string
	relative   string
	uri        string
	languageID string
	text       string
}

func readDocument(root, requested string, profile language.Profile, maximum int64) (document, error) {
	requested = strings.TrimSpace(strings.ReplaceAll(requested, "\\", "/"))
	if requested == "" || filepath.IsAbs(requested) || requested == "." || requested == ".." || strings.HasPrefix(requested, "../") || strings.ContainsAny(requested, "\x00\r\n:") {
		return document{}, errors.New("query language server: path must be a normalized worktree-relative file")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(requested)))
	if clean != requested {
		return document{}, errors.New("query language server: path must be normalized")
	}
	if _, supported := (language.WorkspaceProfile{Languages: []language.Profile{profile}}).ResolvePath(clean); !supported {
		return document{}, errors.New("query language server: file extension does not match the selected language")
	}
	joined := filepath.Join(root, filepath.FromSlash(clean))
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil || !withinRoot(root, resolved) || !samePath(joined, resolved) {
		return document{}, errors.New("query language server: file is unavailable or crosses a symlink boundary")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return document{}, errors.New("query language server: file is unreadable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return document{}, fmt.Errorf("query language server: file exceeds the %d-byte regular-file limit", maximum)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) > maximum {
		return document{}, errors.New("query language server: file could not be read")
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return document{}, errors.New("query language server: file is not UTF-8 text")
	}
	return document{path: resolved, relative: clean, uri: fileURI(resolved), languageID: language.DocumentLanguage(profile.ID, clean), text: string(content)}, nil
}

func fileURI(value string) string {
	path := filepath.ToSlash(value)
	if len(path) >= 2 && path[1] == ':' {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func pathFromFileURI(root, value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" || parsed.Host != "" {
		return "", false
	}
	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return "", false
	}
	if len(decoded) >= 3 && decoded[0] == '/' && decoded[2] == ':' {
		decoded = decoded[1:]
	}
	candidate := filepath.Clean(filepath.FromSlash(decoded))
	if !withinRoot(root, candidate) {
		return "", false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func withinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
