package workspace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIndexFilesRespectsGitIgnoreAndDependencyBoundaries(t *testing.T) {
	root := committedRepository(t)
	files := map[string]string{
		".gitignore":                   "ignored.txt\ncache/\n",
		"src/keep.go":                  "package src\n",
		"ignored.txt":                  "ignored\n",
		"cache/generated.go":           "package cache\n",
		"vendor/tracked.go":            "package vendor\n",
		"node_modules/module/index.js": "export {}\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", "-f", ".gitignore", "src/keep.go", "vendor/tracked.go", "node_modules/module/index.js")
	values, truncated, err := IndexFiles(context.Background(), root, ".", FileIndexOptions{MaxFiles: 100})
	if err != nil || truncated {
		t.Fatalf("index files: values=%#v truncated=%v err=%v", values, truncated, err)
	}
	want := []string{".gitignore", "main.go", "src/keep.go"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("indexed files = %#v, want %#v", values, want)
	}
	subtree, _, err := IndexFiles(context.Background(), root, "src", FileIndexOptions{MaxFiles: 100})
	if err != nil || !reflect.DeepEqual(subtree, []string{"src/keep.go"}) {
		t.Fatalf("subtree = %#v, %v", subtree, err)
	}
}

func TestIndexFilesAppliesFilterBeforeBoundAndReportsTruncation(t *testing.T) {
	root := committedRepository(t)
	for _, name := range []string{"a.txt", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	values, truncated, err := IndexFiles(context.Background(), root, ".", FileIndexOptions{MaxFiles: 1, Include: func(relative string) bool { return filepath.Ext(relative) == ".go" }})
	if err != nil || !truncated || len(values) != 1 || filepath.Ext(values[0]) != ".go" {
		t.Fatalf("filtered index = %#v truncated=%v err=%v", values, truncated, err)
	}
}
