package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/provider"
	openaiadapter "github.com/eaglc/codepilot/internal/provider/openai"
)

func TestProductProviderManagerConfiguresCredentialAndDiscoversModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer top-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"coding-model"}]}`))
	}))
	defer server.Close()
	profiles := provider.NewMemoryProfileRepository()
	credentials := provider.NewMemoryCredentialStore()
	service, err := provider.NewService(profiles, credentials, openaiadapter.New(server.Client()))
	if err != nil {
		t.Fatalf("create Provider service: %v", err)
	}
	manager := &productProviderManager{profiles: profiles, service: service}
	configured, err := manager.ConfigureProfile(context.Background(), codingagent.ConfigureProviderRequest{
		Kind: "openai", DisplayName: "Coding API", BaseURL: server.URL, DefaultModel: "coding-model", Credential: []byte("top-secret"),
	})
	if err != nil {
		t.Fatalf("configure profile: %v", err)
	}
	if configured.ID == "" || !configured.RequiresCredential || !configured.CredentialConfigured {
		t.Fatalf("unexpected configured profile: %#v", configured)
	}
	stored, err := profiles.LoadProfile(context.Background(), provider.ProfileID(configured.ID))
	if err != nil {
		t.Fatalf("load configured profile: %v", err)
	}
	if stored.CredentialRef != configured.ID || strings.Contains(stored.DisplayName+stored.BaseURL+stored.DefaultModel+stored.CredentialRef, "top-secret") {
		t.Fatalf("secret leaked into profile: %#v", stored)
	}
	models, err := manager.ListModels(context.Background(), configured.ID)
	if err != nil || len(models) != 1 || models[0].ID != "coding-model" {
		t.Fatalf("discover models: models=%#v err=%v", models, err)
	}
	if err := manager.ValidateSelection(context.Background(), configured.ID, "coding-model"); err != nil {
		t.Fatalf("validate selection: %v", err)
	}
	listed, err := manager.ListProfiles(context.Background())
	if err != nil || len(listed) != 1 || !listed[0].CredentialConfigured || listed[0].ValidatedAt.IsZero() {
		t.Fatalf("list profiles: profiles=%#v err=%v", listed, err)
	}
}
