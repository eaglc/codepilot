package language

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	pythonCheckTimeout = 5 * time.Minute
	pythonCheckOutput  = 2 << 20
)

// PythonStrategy detects Python project metadata and supplies pytest plans
// without probing, installing, or otherwise changing the Python environment.
type PythonStrategy struct{}

// NewPythonStrategy creates the stateless Python language strategy.
func NewPythonStrategy() *PythonStrategy {
	return &PythonStrategy{}
}

// ID returns the stable Python language identifier.
func (*PythonStrategy) ID() LanguageID {
	return LanguagePython
}

// Detect scores explicit Python and pytest metadata above root-level Python
// source. Detection performs filesystem reads only and never starts Python.
func (*PythonStrategy) Detect(ctx context.Context, root string) (Detection, error) {
	if err := ctx.Err(); err != nil {
		return Detection{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return Detection{}, err
	}
	if !info.IsDir() {
		return Detection{}, errors.New("detect Python: worktree root is not a directory")
	}

	markers := []struct {
		name  string
		score int
	}{
		{name: "pyproject.toml", score: 100},
		{name: "pytest.ini", score: 100},
		{name: "setup.cfg", score: 90},
		{name: "setup.py", score: 90},
		{name: "tox.ini", score: 90},
		{name: "requirements.txt", score: 80},
		{name: "Pipfile", score: 80},
	}
	detection := Detection{}
	for _, marker := range markers {
		if err := ctx.Err(); err != nil {
			return Detection{}, err
		}
		if !fileExists(filepath.Join(root, marker.name)) {
			continue
		}
		if marker.score > detection.Score {
			detection.Score = marker.score
			detection.Evidence = detection.Evidence[:0]
		}
		if marker.score == detection.Score {
			detection.Evidence = append(detection.Evidence, marker.name)
		}
	}
	if detection.Score > 0 {
		return detection, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return Detection{}, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Detection{}, err
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".py" {
			return Detection{Score: 60, Evidence: []string{entry.Name()}}, nil
		}
	}
	return Detection{}, nil
}

// BuildProfile returns bounded pytest plans that rely only on the project's
// existing Python environment. Missing Python or pytest remains an explicit
// check-unavailable result from the command boundary.
func (*PythonStrategy) BuildProfile(ctx context.Context, root string) (LanguageProfile, error) {
	if err := ctx.Err(); err != nil {
		return LanguageProfile{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return LanguageProfile{}, err
	}
	if !info.IsDir() {
		return LanguageProfile{}, errors.New("build Python profile: worktree root is not a directory")
	}

	return LanguageProfile{
		ID:         LanguagePython,
		PromptHint: "This is a Python worktree. Prefer small idiomatic changes, preserve the project's existing style and environment, and use a trusted pytest plan after editing. Never add or install pytest, formatters, linters, or other dependencies.",
		CheckPlans: []CheckPlan{
			pythonCheckPlan("pytest-all", "Run the project's complete pytest suite with concise output.", "-m", "pytest", "-q"),
		},
	}, nil
}

func pythonCheckPlan(id string, description string, arguments ...string) CheckPlan {
	return CheckPlan{
		ID:          id,
		Description: description,
		Command: CheckCommand{
			ID:             id,
			Program:        "python",
			Args:           append([]string(nil), arguments...),
			EnvAllowlist:   []string{"PYTHONDONTWRITEBYTECODE", "PYTHONUTF8", "VIRTUAL_ENV"},
			Timeout:        pythonCheckTimeout,
			MaxOutputBytes: pythonCheckOutput,
		},
	}
}
