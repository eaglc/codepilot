package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/provider"
)

func TestRepositoryPersistsUpdatesAndStableOrder(t *testing.T) {
	root := t.TempDir()
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	ctx := context.Background()
	validatedAt := time.Now().UTC().Truncate(time.Nanosecond)
	profiles := []provider.Profile{
		{ID: "z-local", Kind: provider.KindOllama, DisplayName: "Local", BaseURL: "http://127.0.0.1:11434/", DefaultModel: "qwen-coder", ValidatedAt: validatedAt},
		{ID: "a-openai", Kind: provider.KindOpenAI, DisplayName: "OpenAI", BaseURL: "https://api.openai.com/v1", DefaultModel: "model-a", CredentialRef: "keyring.openai"},
	}
	for _, profile := range profiles {
		if err := repository.SaveProfile(ctx, profile); err != nil {
			t.Fatalf("save profile %q: %v", profile.ID, err)
		}
	}
	reopened, err := NewRepository(root)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	listed, err := reopened.ListProfiles(ctx)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != "a-openai" || listed[1].ID != "z-local" || listed[1].BaseURL != "http://127.0.0.1:11434" || !listed[1].ValidatedAt.Equal(validatedAt) {
		t.Fatalf("listed profiles = %#v", listed)
	}
	updated := listed[0]
	updated.DefaultModel = "model-b"
	if err := reopened.SaveProfile(ctx, updated); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	loaded, err := repository.LoadProfile(ctx, updated.ID)
	if err != nil || loaded.DefaultModel != "model-b" {
		t.Fatalf("load updated profile = %#v, err=%v", loaded, err)
	}
}

func TestRepositoryAcceptsLegacyOpaqueCredentialReference(t *testing.T) {
	repository, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	profile := provider.Profile{ID: "legacy", Kind: provider.KindOpenAI, DisplayName: "Legacy", DefaultModel: "model", CredentialRef: "provider/prv_test"}
	if err := repository.SaveProfile(context.Background(), profile); err != nil {
		t.Fatalf("save legacy reference: %v", err)
	}
	loaded, err := repository.LoadProfile(context.Background(), profile.ID)
	if err != nil || loaded.CredentialRef != profile.CredentialRef {
		t.Fatalf("load legacy reference: profile=%#v err=%v", loaded, err)
	}
}

func TestRepositoryRejectsInvalidAndCorruptProfiles(t *testing.T) {
	tests := []provider.Profile{
		{ID: "../escape", Kind: provider.KindOpenAI, DisplayName: "OpenAI", DefaultModel: "model"},
		{ID: "profile", Kind: provider.KindOpenAI, DisplayName: "OpenAI", BaseURL: "file:///secret", DefaultModel: "model"},
		{ID: "profile", Kind: provider.KindOpenAI, DisplayName: "", DefaultModel: "model"},
		{ID: "profile", Kind: provider.KindOpenAI, DisplayName: "OpenAI", DefaultModel: "", CredentialRef: "bad ref"},
	}
	for index, profile := range tests {
		repository, err := NewRepository(t.TempDir())
		if err != nil {
			t.Fatalf("case %d create repository: %v", index, err)
		}
		if err := repository.SaveProfile(context.Background(), profile); err == nil {
			t.Fatalf("case %d accepted invalid profile %#v", index, profile)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, fileName), []byte(`{"version":1,"profiles":[{"id":"same","kind":"openai","display_name":"A","default_model":"m"},{"id":"same","kind":"openai","display_name":"B","default_model":"m"}]}`), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	repository, _ := NewRepository(root)
	if _, err := repository.ListProfiles(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate file error = %v", err)
	}
}

func TestRepositoryNeverPersistsCredentialMaterial(t *testing.T) {
	root := t.TempDir()
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	profile := provider.Profile{
		ID: "openai", Kind: provider.KindOpenAI, DisplayName: "OpenAI", DefaultModel: "model", CredentialRef: "keyring.openai",
	}
	if err := repository.SaveProfile(context.Background(), profile); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, fileName))
	if err != nil {
		t.Fatalf("read profile file: %v", err)
	}
	text := string(content)
	if strings.Contains(text, "sk-secret-value") || strings.Contains(strings.ToLower(text), "api_key") || !strings.Contains(text, `"credential_ref": "keyring.openai"`) {
		t.Fatalf("profile file contains unexpected fields: %s", text)
	}
}

func TestRepositoryRejectsUnknownFieldsAndUnsupportedVersion(t *testing.T) {
	for _, content := range []string{
		`{"version":2,"profiles":[]}`,
		`{"version":1,"profiles":[],"unknown":true}`,
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, fileName), []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		repository, _ := NewRepository(root)
		if _, err := repository.ListProfiles(context.Background()); err == nil {
			t.Fatalf("accepted invalid file %s", content)
		}
	}
}
