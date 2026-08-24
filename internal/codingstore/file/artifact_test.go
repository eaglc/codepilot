package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func TestRepositoryStoresContentAddressedArtifactWithPrivateReference(t *testing.T) {
	root := t.TempDir()
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	artifact := codingagent.Artifact{MediaType: "text/plain; charset=utf-8", Data: []byte("bounded check output")}
	first, err := repository.SaveArtifact(context.Background(), artifact)
	if err != nil {
		t.Fatalf("save artifact: %v", err)
	}
	second, err := repository.SaveArtifact(context.Background(), artifact)
	if err != nil || second != first {
		t.Fatalf("save duplicate artifact: first=%#v second=%#v err=%v", first, second, err)
	}
	if !strings.HasPrefix(first.ID, "sha256:") || first.Size != int64(len(artifact.Data)) {
		t.Fatalf("artifact reference = %#v", first)
	}
	path := filepath.Join(root, "coding-artifacts", strings.TrimPrefix(first.ID, "sha256:")+".blob")
	content, err := os.ReadFile(path)
	if err != nil || string(content) != string(artifact.Data) {
		t.Fatalf("read artifact: content=%q err=%v", content, err)
	}
	loaded, err := repository.LoadArtifact(context.Background(), first)
	if err != nil || loaded.MediaType != artifact.MediaType || string(loaded.Data) != string(artifact.Data) {
		t.Fatalf("load verified artifact: loaded=%#v err=%v", loaded, err)
	}
	tampered := first
	tampered.Size++
	if _, err := repository.LoadArtifact(context.Background(), tampered); err == nil {
		t.Fatal("load accepted a mismatched artifact reference")
	}
}
