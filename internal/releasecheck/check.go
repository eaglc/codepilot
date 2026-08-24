// Package releasecheck verifies the deterministic build inputs used by the
// CodePilot release workflow.
package releasecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/buildinfo"
)

var (
	semanticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
	commitPattern          = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
)

// Metadata is the immutable identity embedded in every binary of one release.
type Metadata struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

// Validate rejects metadata that cannot be safely and deterministically passed
// to the Go linker.
func (m Metadata) Validate() error {
	if !semanticVersionPattern.MatchString(m.Version) {
		return fmt.Errorf("version %q is not semantic version syntax without a leading v", m.Version)
	}
	if !commitPattern.MatchString(m.Commit) {
		return fmt.Errorf("commit %q is not a full SHA-1 or SHA-256 hexadecimal object ID", m.Commit)
	}
	if _, err := time.Parse(time.RFC3339, m.BuildDate); err != nil {
		return fmt.Errorf("build date %q is not RFC3339: %w", m.BuildDate, err)
	}
	return nil
}

// Target identifies one supported release platform.
type Target struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

func (t Target) name() string {
	return t.GOOS + "/" + t.GOARCH
}

func (t Target) binaryName() string {
	if t.GOOS == "windows" {
		return "codepilot.exe"
	}
	return "codepilot"
}

// Targets returns the complete release matrix in stable order.
func Targets() []Target {
	return []Target{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "windows", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
	}
}

// Artifact records the digest shared by both independent builds of a target.
type Artifact struct {
	Target string `json:"target"`
	SHA256 string `json:"sha256"`
}

// Report is the machine-readable result of a successful release preflight.
type Report struct {
	Metadata  Metadata   `json:"metadata"`
	GoVersion string     `json:"go_version"`
	Artifacts []Artifact `json:"artifacts"`
}

// Options controls repository policy checks in addition to byte-for-byte build
// comparison. Tag releases require both policy switches; local investigation
// may omit them while a change is still uncommitted.
type Options struct {
	Root             string
	Metadata         Metadata
	RequireClean     bool
	RequireChangelog bool
}

// Verify builds every supported target twice in separate directories and
// rejects any byte difference. It also verifies that metadata names the current
// Git commit and uses that commit's timestamp.
func Verify(ctx context.Context, options Options) (Report, error) {
	if err := options.Metadata.Validate(); err != nil {
		return Report{}, err
	}
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve repository root: %w", err)
	}
	if err := verifyRepository(ctx, root, options); err != nil {
		return Report{}, err
	}

	firstDirectory, err := os.MkdirTemp("", "codepilot-release-first-")
	if err != nil {
		return Report{}, fmt.Errorf("create first release build directory: %w", err)
	}
	defer os.RemoveAll(firstDirectory)
	secondDirectory, err := os.MkdirTemp("", "codepilot-release-second-")
	if err != nil {
		return Report{}, fmt.Errorf("create second release build directory: %w", err)
	}
	defer os.RemoveAll(secondDirectory)

	report := Report{Metadata: options.Metadata, GoVersion: runtime.Version()}
	for _, target := range Targets() {
		firstPath := filepath.Join(firstDirectory, target.GOOS+"-"+target.GOARCH, target.binaryName())
		secondPath := filepath.Join(secondDirectory, target.GOOS+"-"+target.GOARCH, target.binaryName())
		if err := build(ctx, root, firstPath, target, options.Metadata); err != nil {
			return Report{}, err
		}
		if err := build(ctx, root, secondPath, target, options.Metadata); err != nil {
			return Report{}, err
		}
		firstDigest, err := fileDigest(firstPath)
		if err != nil {
			return Report{}, fmt.Errorf("digest first %s build: %w", target.name(), err)
		}
		secondDigest, err := fileDigest(secondPath)
		if err != nil {
			return Report{}, fmt.Errorf("digest second %s build: %w", target.name(), err)
		}
		if firstDigest != secondDigest {
			return Report{}, fmt.Errorf("%s build is not reproducible: %s differs from %s", target.name(), firstDigest, secondDigest)
		}
		if target.GOOS == runtime.GOOS && target.GOARCH == runtime.GOARCH {
			if err := verifyVersionOutput(ctx, firstPath, options.Metadata); err != nil {
				return Report{}, err
			}
		}
		report.Artifacts = append(report.Artifacts, Artifact{Target: target.name(), SHA256: firstDigest})
	}
	return report, nil
}

func verifyRepository(ctx context.Context, root string, options Options) error {
	for _, name := range []string{"go.mod", "CHANGELOG.md", filepath.Join("cmd", "codepilot", "main.go")} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("release repository is missing regular file %s", name)
		}
	}
	head, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if !strings.EqualFold(head, options.Metadata.Commit) {
		return fmt.Errorf("release commit %s does not match repository HEAD %s", options.Metadata.Commit, head)
	}
	commitDate, err := gitOutput(ctx, root, "show", "-s", "--format=%cI", head)
	if err != nil {
		return err
	}
	wantDate, _ := time.Parse(time.RFC3339, options.Metadata.BuildDate)
	actualDate, err := time.Parse(time.RFC3339, commitDate)
	if err != nil {
		return fmt.Errorf("parse Git commit date %q: %w", commitDate, err)
	}
	if !wantDate.Equal(actualDate) {
		return fmt.Errorf("release build date %s does not match commit date %s", options.Metadata.BuildDate, commitDate)
	}
	if options.RequireClean {
		status, statusErr := gitOutput(ctx, root, "status", "--porcelain", "--untracked-files=all")
		if statusErr != nil {
			return statusErr
		}
		if status != "" {
			return errors.New("release repository is not clean")
		}
	}
	if options.RequireChangelog {
		content, readErr := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
		if readErr != nil {
			return fmt.Errorf("read release changelog: %w", readErr)
		}
		heading := fmt.Sprintf("## [%s] - %s", options.Metadata.Version, wantDate.Format("2006-01-02"))
		if !strings.Contains(string(content), heading) {
			return fmt.Errorf("CHANGELOG.md is missing exact release heading %q", heading)
		}
	}
	return nil
}

func gitOutput(ctx context.Context, root string, arguments ...string) (string, error) {
	commandArguments := append([]string{"-C", root}, arguments...)
	output, err := exec.CommandContext(ctx, "git", commandArguments...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run git %s: %w: %s", strings.Join(arguments, " "), err, boundedOutput(output))
	}
	return strings.TrimSpace(string(output)), nil
}

func build(ctx context.Context, root string, outputPath string, target Target, metadata Metadata) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create %s build directory: %w", target.name(), err)
	}
	linkerFlags := strings.Join([]string{
		"-s", "-w", "-buildid=",
		"-X", "main.version=" + metadata.Version,
		"-X", "main.commit=" + metadata.Commit,
		"-X", "main.buildDate=" + metadata.BuildDate,
	}, " ")
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", linkerFlags, "-o", outputPath, "./cmd/codepilot")
	command.Dir = root
	command.Env = buildEnvironment(target)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build %s: %w: %s", target.name(), err, boundedOutput(output))
	}
	return nil
}

func buildEnvironment(target Target) []string {
	overrides := map[string]string{
		"CGO_ENABLED": "0",
		"GOOS":        target.GOOS,
		"GOARCH":      target.GOARCH,
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := overrides[strings.ToUpper(name)]; replaced {
				continue
			}
		}
		environment = append(environment, entry)
	}
	for _, name := range []string{"CGO_ENABLED", "GOOS", "GOARCH"} {
		environment = append(environment, name+"="+overrides[name])
	}
	return environment
}

func fileDigest(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func verifyVersionOutput(ctx context.Context, binaryPath string, metadata Metadata) error {
	output, err := exec.CommandContext(ctx, binaryPath, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("run native release binary: %w: %s", err, boundedOutput(output))
	}
	want := buildinfo.Format(metadata.Version, metadata.Commit, metadata.BuildDate)
	if got := strings.TrimSpace(string(output)); got != want {
		return fmt.Errorf("native release version output %q, want %q", got, want)
	}
	return nil
}

func boundedOutput(output []byte) string {
	const limit = 4 << 10
	if len(output) > limit {
		return strings.TrimSpace(string(output[:limit])) + "..."
	}
	return strings.TrimSpace(string(output))
}
