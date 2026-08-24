package architecture_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCIWorkflowKeepsNativePlatformsAndOfflineSafetyGates(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	var workflow yaml.Node
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	text := string(content)
	required := []string{
		"ubuntu-latest", "windows-latest", "macos-latest",
		"CODEPILOT_REQUIRE_PYTHON_E2E", "go test ./... -count=1",
		"go test ./internal/ui ./internal/sessionstore/file -count=3",
		"go test -race", "go vet ./...", "go build -trimpath",
		"GOOS: ${{ matrix.goos }}", "GOARCH: ${{ matrix.goarch }}",
	}
	for _, value := range required {
		if !strings.Contains(text, value) {
			t.Fatalf("CI workflow is missing required platform/quality gate %q", value)
		}
	}
	if strings.Contains(text, "CODEPILOT_LIVE_OPENAI:") || strings.Contains(text, "CODEPILOT_LIVE_DEEPSEEK:") || strings.Contains(text, "CODEPILOT_LIVE_OLLAMA:") {
		t.Fatal("untrusted CI must not enable real Provider tests")
	}
}
