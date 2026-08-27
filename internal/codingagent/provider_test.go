package codingagent_test

import (
	"context"
	"testing"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingmemory "github.com/eaglc/codepilot/internal/codingstore/memory"
	"github.com/eaglc/codepilot/internal/contextmanager"
)

type selectionProviderManager struct {
	profileID string
	modelID   string
}

func (*selectionProviderManager) ListProfiles(context.Context) ([]codingagent.ProviderProfile, error) {
	return nil, nil
}
func (*selectionProviderManager) ConfigureProfile(context.Context, codingagent.ConfigureProviderRequest) (codingagent.ProviderProfile, error) {
	return codingagent.ProviderProfile{}, nil
}
func (*selectionProviderManager) ListModels(context.Context, string) ([]codingagent.ProviderModel, error) {
	return nil, nil
}
func (m *selectionProviderManager) ValidateSelection(_ context.Context, profileID, modelID string) error {
	m.profileID, m.modelID = profileID, modelID
	return nil
}

func TestSelectProviderModelValidatesPersistsAndPublishesProductEvent(t *testing.T) {
	productStore := codingmemory.NewRepository()
	seedMemoryWorktree(t, productStore, "workspace", "worktree")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: finalModelFactory{}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatalf("create Agent runtime: %v", err)
	}
	providers := &selectionProviderManager{}
	events := &productEvents{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, Turns: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events, Providers: providers,
	})
	if err != nil {
		t.Fatalf("create Coding Agent service: %v", err)
	}
	created, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding", AgentSessionID: "agent", WorkspaceID: "workspace", WorktreeID: "worktree",
		ProviderProfileID: "old-profile", ModelID: "old-model", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create Coding session: %v", err)
	}
	selected, err := service.SelectProviderModel(context.Background(), created.ID, "new-profile", "new-model")
	if err != nil {
		t.Fatalf("select Provider model: %v", err)
	}
	if providers.profileID != "new-profile" || providers.modelID != "new-model" || selected.ProviderProfileID != "new-profile" || selected.ModelID != "new-model" {
		t.Fatalf("selection was not validated and returned: provider=%#v session=%#v", providers, selected)
	}
	stored, err := productStore.LoadSession(context.Background(), created.ID)
	if err != nil || stored.ProviderProfileID != "new-profile" || stored.ModelID != "new-model" {
		t.Fatalf("selection was not persisted: session=%#v err=%v", stored, err)
	}
	if len(events.values) != 1 || events.values[0].Kind != codingagent.EventSessionUpdated || events.values[0].Payload.Session == nil || events.values[0].Payload.Session.ModelID != "new-model" {
		t.Fatalf("session update event = %#v", events.values)
	}
}
