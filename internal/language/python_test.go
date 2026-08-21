package language

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eaglc/codepilot/internal/agent"
)

func TestRegistryResolveLanguageSelectsPythonProject(t *testing.T) {
	root := t.TempDir()
	writeLanguageTestFile(t, root, "pyproject.toml", "[tool.pytest.ini_options]\naddopts = \"-q\"\n")
	registry, err := NewRegistry(NewGoStrategy(), NewPythonStrategy())
	if err != nil {
		t.Fatal(err)
	}

	profile, err := registry.ResolveLanguage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != agent.LanguagePython {
		t.Fatalf("language = %q, want %q", profile.ID, agent.LanguagePython)
	}
	if len(profile.CheckPlans) != 1 {
		t.Fatalf("plans = %#v", profile.CheckPlans)
	}
	plan := profile.CheckPlans[0]
	if plan.ID != "pytest-all" || plan.Command.Program != "python" || !reflect.DeepEqual(plan.Command.Args, []string{"-m", "pytest", "-q"}) {
		t.Fatalf("pytest plan = %#v", plan)
	}
}

func TestPythonStrategyKeepsPytestTargetInsideFixedProjectPlan(t *testing.T) {
	root := t.TempDir()
	writeLanguageTestFile(t, root, "pyproject.toml", "[project]\nname = \"fixture\"\n")
	if err := os.Mkdir(filepath.Join(root, "tests"), 0o700); err != nil {
		t.Fatal(err)
	}

	profile, err := NewPythonStrategy().BuildProfile(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.CheckPlans) != 1 {
		t.Fatalf("plans = %#v", profile.CheckPlans)
	}
	plan := profile.CheckPlans[0]
	if plan.ID != "pytest-all" || !reflect.DeepEqual(plan.Command.Args, []string{"-m", "pytest", "-q"}) {
		t.Fatalf("project plan = %#v", plan)
	}
}

func TestPythonStrategyDetectsRootSourceWithoutManifest(t *testing.T) {
	root := t.TempDir()
	writeLanguageTestFile(t, root, "answer.py", "def answer():\n    return 42\n")

	detection, err := NewPythonStrategy().Detect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Score != 60 || !reflect.DeepEqual(detection.Evidence, []string{"answer.py"}) {
		t.Fatalf("detection = %#v", detection)
	}
}

func TestRegistryResolveLanguageFallsBackForEqualGoAndPythonMetadata(t *testing.T) {
	root := t.TempDir()
	writeLanguageTestFile(t, root, "go.mod", "module example.test/mixed\n")
	writeLanguageTestFile(t, root, "pyproject.toml", "[project]\nname = \"mixed\"\n")
	registry, err := NewRegistry(NewGoStrategy(), NewPythonStrategy())
	if err != nil {
		t.Fatal(err)
	}

	profile, err := registry.ResolveLanguage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != agent.LanguageGeneric || len(profile.CheckPlans) != 0 {
		t.Fatalf("profile = %#v, want deterministic generic fallback", profile)
	}
}

func writeLanguageTestFile(t *testing.T, root string, relative string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, relative), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
