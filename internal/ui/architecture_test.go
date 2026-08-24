package ui

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPresentationPackageDoesNotImportLowerRuntimeLayers(t *testing.T) {
	forbidden := []string{
		"github.com/eaglc/codepilot/internal/llm",
		"github.com/eaglc/codepilot/internal/provider",
		"github.com/eaglc/codepilot/internal/agent",
		"github.com/eaglc/codepilot/internal/sessionstore",
		"github.com/cloudwego/eino",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list UI files: %v", err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", path, err)
			}
			for _, prefix := range forbidden {
				if value == prefix || strings.HasPrefix(value, prefix+"/") {
					t.Errorf("%s imports forbidden lower-layer package %s", path, value)
				}
			}
		}
	}
}
