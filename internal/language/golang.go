package language

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
)

const (
	goCheckTimeout = 5 * time.Minute
	goCheckOutput  = 2 << 20
)

// GoStrategy detects Go module/workspace metadata and supplies safe Go check
// plans. It never invokes the Go toolchain during detection.
type GoStrategy struct{}

// NewGoStrategy creates the stateless Go language strategy.
func NewGoStrategy() *GoStrategy {
	return &GoStrategy{}
}

// ID returns the stable Go language identifier.
func (*GoStrategy) ID() agent.LanguageID {
	return agent.LanguageGo
}

// Detect scores explicit Go module metadata above a root-level Go source file.
func (*GoStrategy) Detect(ctx context.Context, root string) (Detection, error) {
	if err := ctx.Err(); err != nil {
		return Detection{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return Detection{}, err
	}
	if !info.IsDir() {
		return Detection{}, errors.New("detect Go: worktree root is not a directory")
	}
	if fileExists(filepath.Join(root, "go.work")) {
		return Detection{Score: 110, Evidence: []string{"go.work"}}, nil
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		return Detection{Score: 100, Evidence: []string{"go.mod"}}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return Detection{}, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Detection{}, err
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".go" {
			return Detection{Score: 60, Evidence: []string{entry.Name()}}, nil
		}
	}
	return Detection{}, nil
}

// BuildProfile returns immutable, allowlisted commands for broad tests, a
// root-package test, and static analysis.
func (*GoStrategy) BuildProfile(ctx context.Context, root string) (agent.LanguageProfile, error) {
	if err := ctx.Err(); err != nil {
		return agent.LanguageProfile{}, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		if err != nil {
			return agent.LanguageProfile{}, err
		}
		return agent.LanguageProfile{}, errors.New("build Go profile: worktree root is not a directory")
	}
	return agent.LanguageProfile{
		ID:         agent.LanguageGo,
		PromptHint: "This is a Go worktree. Prefer small idiomatic changes, preserve package boundaries, format changed Go files, and use a trusted Go test plan after editing.",
		CheckPlans: []agent.CheckPlan{
			goCheckPlan("go-test-all", "Run all Go package tests.", "test", "./..."),
			goCheckPlan("go-test-root", "Run tests for the root Go package.", "test", "."),
			goCheckPlan("go-vet-all", "Run Go static analysis for all packages.", "vet", "./..."),
		},
	}, nil
}

func goCheckPlan(id string, description string, arguments ...string) agent.CheckPlan {
	return agent.CheckPlan{
		ID:          id,
		Description: description,
		Command: agent.CheckCommand{
			ID:             id,
			Program:        "go",
			Args:           append([]string(nil), arguments...),
			EnvAllowlist:   []string{"CGO_ENABLED", "GOCACHE", "GOENV", "GOMODCACHE", "GONOPROXY", "GONOSUMDB", "GOPATH", "GOPRIVATE", "GOPROXY", "GOROOT", "GOSUMDB", "GOTMPDIR"},
			Timeout:        goCheckTimeout,
			MaxOutputBytes: goCheckOutput,
		},
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
