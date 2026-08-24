package language

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidateServerUsesOneExactAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		profile Profile
		wantErr bool
	}{
		{name: "go", profile: GoStrategy{}.Profile()},
		{name: "python alternative", profile: Profile{ID: Python, Server: Server{Program: "basedpyright-langserver", Args: []string{"--stdio"}}}},
		{name: "path", profile: Profile{ID: Go, Server: Server{Program: filepath.Join("bin", "gopls"), Args: []string{"serve"}}}, wantErr: true},
		{name: "arguments", profile: Profile{ID: Go, Server: Server{Program: "gopls", Args: []string{"-remote=auto"}}}, wantErr: true},
		{name: "language mismatch", profile: Profile{ID: Python, Server: Server{Program: "gopls", Args: []string{"serve"}}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateServer(test.profile)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateServer() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
	profile := GoStrategy{}.Profile()
	if got, want := ServerBinding(profile), "gopls\x00serve"; got != want {
		t.Fatalf("ServerBinding() = %q, want %q", got, want)
	}
}

func TestDefaultRegistryDetectsPolyglotWorktreeDeterministically(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod": "module example.test/project\n", "pyproject.toml": "[project]\nname='fixture'\n", "package.json": `{"name":"fixture"}`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := NewDefaultRegistry().Detect(context.Background(), root)
	if err != nil {
		t.Fatalf("detect languages: %v", err)
	}
	want := []ID{Node, Python, Go}
	got := make([]ID, len(profile.Languages))
	for index := range profile.Languages {
		got[index] = profile.Languages[index].ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detected languages = %v, want %v", got, want)
	}
	if resolved, ok := profile.ResolvePath("web/app.tsx"); !ok || resolved.ID != Node || DocumentLanguage(resolved.ID, "web/app.tsx") != "typescriptreact" {
		t.Fatalf("resolve TypeScript React path = %#v, %v", resolved, ok)
	}
}

func TestStrategiesUseOnlyRegularMarkersAndRootSources(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "package.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := NewDefaultRegistry().Detect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Languages) != 1 || profile.Languages[0].ID != Python || profile.Languages[0].Score != 60 {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestRegistryDefensivelyCopiesProfiles(t *testing.T) {
	registry := NewDefaultRegistry()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, _ := registry.Detect(context.Background(), root)
	first.Languages[0].Server.Args[0] = "mutated"
	second, _ := registry.Detect(context.Background(), root)
	if second.Languages[0].Server.Args[0] != "serve" {
		t.Fatalf("registry profile was mutable: %#v", second)
	}
}

func TestSourceDetectionUsesGitAwareBoundedIndex(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "-C", root, "init", "--quiet")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize Git fixture: %v: %s", err, output)
	}
	for name, content := range map[string]string{
		".gitignore":           "ignored/\n",
		"src/service.py":       "value = 1\n",
		"ignored/app.ts":       "export {}\n",
		"vendor/dependency.go": "package dependency\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := NewDefaultRegistry().Detect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Languages) != 1 || profile.Languages[0].ID != Python || len(profile.Languages[0].Evidence) != 1 || profile.Languages[0].Evidence[0] != "src/service.py" {
		t.Fatalf("Git-aware source detection = %#v", profile)
	}
}
