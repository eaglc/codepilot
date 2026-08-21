package language

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/eaglc/codepilot/internal/agent"
)

func TestRegistryResolveLanguageSelectsGoModule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/project\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(NewGoStrategy())
	if err != nil {
		t.Fatal(err)
	}

	profile, err := registry.ResolveLanguage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != agent.LanguageGo {
		t.Fatalf("language = %q, want %q", profile.ID, agent.LanguageGo)
	}
	wantPlans := []string{"go-test-all", "go-test-root", "go-vet-all"}
	if len(profile.CheckPlans) != len(wantPlans) {
		t.Fatalf("plans = %d, want %d", len(profile.CheckPlans), len(wantPlans))
	}
	for index, want := range wantPlans {
		plan := profile.CheckPlans[index]
		if plan.ID != want || plan.Command.ID != want || plan.Command.Program != "go" {
			t.Fatalf("plan[%d] = %#v, want ID %q and go program", index, plan, want)
		}
	}
}

func TestRegistryResolveLanguageFallsBackWhenNoStrategyMatches(t *testing.T) {
	registry, err := NewRegistry(NewGoStrategy())
	if err != nil {
		t.Fatal(err)
	}

	profile, err := registry.ResolveLanguage(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != agent.LanguageGeneric || len(profile.CheckPlans) != 0 {
		t.Fatalf("profile = %#v, want generic without checks", profile)
	}
}

func TestRegistryResolveLanguageFallsBackOnTiedScore(t *testing.T) {
	registry, err := NewRegistry(
		&fakeStrategy{id: agent.LanguageGo, detection: Detection{Score: 80}},
		&fakeStrategy{id: agent.LanguagePython, detection: Detection{Score: 80}},
	)
	if err != nil {
		t.Fatal(err)
	}

	profile, err := registry.ResolveLanguage(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != agent.LanguageGeneric {
		t.Fatalf("language = %q, want %q", profile.ID, agent.LanguageGeneric)
	}
}

func TestRegistryResolveLanguagePropagatesDetectionError(t *testing.T) {
	wantErr := errors.New("detection failed")
	registry, err := NewRegistry(&fakeStrategy{id: agent.LanguageGo, detectErr: wantErr})
	if err != nil {
		t.Fatal(err)
	}

	_, err = registry.ResolveLanguage(context.Background(), t.TempDir())
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestGoStrategyDetectsRootGoSourceWithoutManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	detection, err := NewGoStrategy().Detect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Score != 60 || len(detection.Evidence) != 1 || detection.Evidence[0] != "main.go" {
		t.Fatalf("detection = %#v", detection)
	}
}

type fakeStrategy struct {
	id        agent.LanguageID
	detection Detection
	detectErr error
}

func (s *fakeStrategy) ID() agent.LanguageID {
	return s.id
}

func (s *fakeStrategy) Detect(context.Context, string) (Detection, error) {
	return s.detection, s.detectErr
}

func (s *fakeStrategy) BuildProfile(context.Context, string) (agent.LanguageProfile, error) {
	return agent.LanguageProfile{ID: s.id, PromptHint: "test profile"}, nil
}
