package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
)

func TestProviderFileStore_RoundTripReplaceAndDelete(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "providers.yaml")
	store := NewProviderFileStore(path)
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	first := provider.Profile{
		ID:            "prv_openai",
		Kind:          provider.KindOpenAI,
		DisplayName:   "OpenAI",
		ModelID:       "gpt-test",
		CredentialRef: "keyring:provider/prv_openai",
		ValidatedAt:   now,
	}
	second := provider.Profile{
		ID:          "prv_local",
		Kind:        provider.KindOllama,
		DisplayName: "Local Ollama",
		BaseURL:     "http://127.0.0.1:11434",
		ModelID:     "qwen-coder",
		ValidatedAt: now.Add(time.Minute),
	}

	if err := store.SaveProfile(ctx, first); err != nil {
		t.Fatalf("save first profile: %v", err)
	}
	if err := store.SaveProfile(ctx, second); err != nil {
		t.Fatalf("save second profile: %v", err)
	}
	first.ModelID = "gpt-updated"
	if err := store.SaveProfile(ctx, first); err != nil {
		t.Fatalf("replace first profile: %v", err)
	}

	profiles, err := NewProviderFileStore(path).ListProfiles(ctx)
	if err != nil {
		t.Fatalf("reopen provider file: %v", err)
	}
	if len(profiles) != 2 || profiles[0].ModelID != first.ModelID || profiles[1] != second {
		t.Fatalf("unexpected profiles: %#v", profiles)
	}
	loaded, err := store.LoadProfile(ctx, first.ID)
	if err != nil {
		t.Fatalf("load first profile: %v", err)
	}
	if loaded != first {
		t.Fatalf("loaded %#v, want %#v", loaded, first)
	}

	if err := store.DeleteProfile(ctx, first.ID); err != nil {
		t.Fatalf("delete first profile: %v", err)
	}
	if _, err := store.LoadProfile(ctx, first.ID); !hasConfigErrorCode(err, session.ErrNotFound) {
		t.Fatalf("load deleted profile error = %v, want not found", err)
	}
	profiles, err = store.ListProfiles(ctx)
	if err != nil || len(profiles) != 1 || profiles[0].ID != second.ID {
		t.Fatalf("profiles after delete = %#v, %v", profiles, err)
	}
}

func TestProviderFileStore_RejectsCredentialValueAndUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.yaml")
	store := NewProviderFileStore(path)
	profile := provider.Profile{
		ID:            "prv_test",
		Kind:          provider.KindDeepSeek,
		DisplayName:   "DeepSeek",
		ModelID:       "deepseek-chat",
		CredentialRef: "sk-test-secret",
		ValidatedAt:   time.Now().UTC(),
	}
	if err := store.SaveProfile(context.Background(), profile); err == nil {
		t.Fatal("raw credential value was accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid profile created providers file: %v", err)
	}

	content := `version: 1
profiles:
  - id: prv_test
    kind: deepseek
    display_name: DeepSeek
    model_id: deepseek-chat
    validated_at: 2026-08-20T08:00:00Z
    api_key: forbidden
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write provider fixture: %v", err)
	}
	if _, err := store.ListProfiles(context.Background()); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("unknown sensitive field error = %v", err)
	}
}

func TestProviderFileStore_RejectsCredentialInBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.yaml")
	store := NewProviderFileStore(path)
	profile := provider.Profile{
		ID:          "prv_custom",
		Kind:        provider.KindOpenAICompatible,
		DisplayName: "Custom",
		BaseURL:     "https://sk-test-secret@example.com/v1",
		ModelID:     "coder-model",
		ValidatedAt: time.Now().UTC(),
	}
	if err := store.SaveProfile(context.Background(), profile); err == nil {
		t.Fatal("credential-bearing base URL was accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid profile created providers file: %v", err)
	}
}

func hasConfigErrorCode(err error, code session.ErrorCode) bool {
	var appError *session.AppError
	return errors.As(err, &appError) && appError.Code == code
}
