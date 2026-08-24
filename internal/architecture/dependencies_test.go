package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const internalImportPrefix = "github.com/eaglc/codepilot/internal/"

// TestInternalDependencyDirection turns the documented layer boundaries into
// an executable rule. The composition root is intentionally unrestricted;
// every other package must explicitly list the lower layers it may consume.
func TestInternalDependencyDirection(t *testing.T) {
	root := filepath.Clean("..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		packagePath := filepath.ToSlash(relative)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		for _, spec := range parsed.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import in %s: %v", path, err)
				continue
			}
			if strings.Contains(imported, "/_legacy/") || strings.HasSuffix(imported, "/_legacy") {
				t.Errorf("%s imports read-only legacy code %s", path, imported)
				continue
			}
			if !strings.HasPrefix(imported, internalImportPrefix) {
				continue
			}
			internalPackage := strings.TrimPrefix(imported, internalImportPrefix)
			if !internalImportAllowed(packagePath, internalPackage) {
				t.Errorf("%s package %q imports forbidden internal layer %q", path, packagePath, internalPackage)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal packages: %v", err)
	}
}

func internalImportAllowed(packagePath, imported string) bool {
	if packagePath == "app" || strings.HasPrefix(packagePath, "app/") {
		return true
	}
	allowed := allowedInternalImports(packagePath)
	for _, prefix := range allowed {
		if imported == prefix || strings.HasPrefix(imported, prefix+"/") {
			return true
		}
	}
	return false
}

func allowedInternalImports(packagePath string) []string {
	switch {
	case packagePath == "llm", strings.HasPrefix(packagePath, "llm/"):
		return nil
	case packagePath == "buildinfo", strings.HasPrefix(packagePath, "buildinfo/"):
		return nil
	case packagePath == "tool", strings.HasPrefix(packagePath, "tool/"):
		return []string{"llm"}
	case packagePath == "contextmanager", strings.HasPrefix(packagePath, "contextmanager/"):
		return []string{"llm"}
	case packagePath == "provider":
		return []string{"llm"}
	case strings.HasPrefix(packagePath, "provider/"):
		return []string{"llm", "provider"}
	case packagePath == "agent/session", strings.HasPrefix(packagePath, "agent/session/"):
		return []string{"llm"}
	case packagePath == "agent", strings.HasPrefix(packagePath, "agent/"):
		return []string{"agent/session", "contextmanager", "llm", "tool"}
	case packagePath == "sessionstore", strings.HasPrefix(packagePath, "sessionstore/"):
		return []string{"agent/session", "contextmanager"}
	case packagePath == "codingagent/workspace", strings.HasPrefix(packagePath, "codingagent/workspace/"):
		return nil
	case packagePath == "codingagent", strings.HasPrefix(packagePath, "codingagent/"):
		return []string{"agent", "codingagent", "llm", "tool"}
	case packagePath == "codingstore", strings.HasPrefix(packagePath, "codingstore/"):
		return []string{"codingagent"}
	case packagePath == "ui", strings.HasPrefix(packagePath, "ui/"):
		return []string{"codingagent"}
	case packagePath == "architecture", strings.HasPrefix(packagePath, "architecture/"):
		return nil
	case packagePath == "releasecheck", strings.HasPrefix(packagePath, "releasecheck/"):
		return []string{"buildinfo"}
	default:
		return nil
	}
}
