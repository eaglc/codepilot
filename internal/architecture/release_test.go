package architecture_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseConfigurationKeepsIdentityPackagingAndSupplyChainGates(t *testing.T) {
	root := filepath.Join("..", "..")
	configuration := readYAMLFile(t, filepath.Join(root, ".goreleaser.yml"))
	workflow := readYAMLFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))

	requiredConfiguration := []string{
		"version: 2", "./cmd/codepilot", "CGO_ENABLED=0",
		"linux", "windows", "darwin", "amd64", "arm64",
		"-trimpath", "-buildvcs=false", "-buildid=",
		"main.version={{ .Version }}", "main.commit={{ .Commit }}", "main.buildDate={{ .CommitDate }}",
		"mod_timestamp: \"{{ .CommitTimestamp }}\"", "tar.gz", "zip",
		"algorithm: sha256", "artifacts: archive", "prerelease: auto",
	}
	for _, value := range requiredConfiguration {
		if !strings.Contains(configuration, value) {
			t.Fatalf("GoReleaser configuration is missing release invariant %q", value)
		}
	}

	requiredWorkflow := []string{
		"tags:", "- \"v*\"", "fetch-depth: 0", "contents: write",
		"CODEPILOT_REQUIRE_PYTHON_E2E", "go test ./... -count=1", "go vet ./...",
		"go run ./cmd/releasecheck", "--require-clean", "--require-changelog",
		"anchore/sbom-action/download-syft@v0", "syft-version: v1.51.0",
		"goreleaser/goreleaser-action@v7", "version: v2.17.1", "release --clean",
	}
	for _, value := range requiredWorkflow {
		if !strings.Contains(workflow, value) {
			t.Fatalf("release workflow is missing release gate %q", value)
		}
	}
	if strings.Contains(workflow, "pull_request:") || strings.Contains(workflow, "CODEPILOT_LIVE_OPENAI:") || strings.Contains(workflow, "CODEPILOT_LIVE_DEEPSEEK:") || strings.Contains(workflow, "CODEPILOT_LIVE_OLLAMA:") {
		t.Fatal("release workflow must be tag-only and must not implicitly spend Provider credentials")
	}

	for _, name := range []string{"CHANGELOG.md", filepath.Join("docs", "release-and-upgrade.md")} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("release documentation is missing %s", name)
		}
	}
}

func readYAMLFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return string(content)
}
