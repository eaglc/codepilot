package codingagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/llm"
)

type instructionContextPrompt struct {
	instructionCalls int
	legacyCalls      int
}

type instructionWorktreeReader struct {
	worktree Worktree
}

func (r instructionWorktreeReader) LoadWorktree(context.Context, WorktreeID) (Worktree, error) {
	return r.worktree, nil
}

type instructionSessionRepository struct {
	session Session
}

func (r instructionSessionRepository) CreateSession(context.Context, Session) error { return nil }
func (r instructionSessionRepository) LoadSession(context.Context, SessionID) (Session, error) {
	return r.session, nil
}
func (r instructionSessionRepository) ListSessions(context.Context) ([]Session, error) {
	return []Session{r.session}, nil
}
func (r instructionSessionRepository) SaveSession(context.Context, Session) error { return nil }
func (r instructionSessionRepository) BeginSessionCreation(context.Context, SessionCreationIntent) error {
	return nil
}
func (r instructionSessionRepository) CompleteSessionCreation(context.Context, SessionCreationIntentID, time.Time) error {
	return nil
}
func (r instructionSessionRepository) ListSessionCreationIntents(context.Context) ([]SessionCreationIntent, error) {
	return nil, nil
}

func (*instructionContextPrompt) BuildSystemPrompt(context.Context, PromptScope) (string, error) {
	return "system", nil
}

func (p *instructionContextPrompt) BuildUntrustedContext(context.Context, PromptScope) ([]llm.Message, error) {
	p.legacyCalls++
	return nil, nil
}

func (p *instructionContextPrompt) BuildInstructionContext(context.Context, PromptScope) ([]llm.Message, InstructionReport, error) {
	p.instructionCalls++
	return []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "instruction context"}}}}, InstructionReport{
		Sources: []InstructionSource{{Source: "AGENTS.md", Scope: ".", SHA256: strings.Repeat("a", 64), Status: InstructionLoaded}},
	}, nil
}

func TestBuildPromptContextUsesSingleInstructionDiscoveryBoundary(t *testing.T) {
	builder := &instructionContextPrompt{}
	_, messages, categories, err := buildPromptContext(context.Background(), builder, PromptScope{})
	if err != nil {
		t.Fatal(err)
	}
	if builder.instructionCalls != 1 || builder.legacyCalls != 0 {
		t.Fatalf("instruction calls=%d legacy calls=%d", builder.instructionCalls, builder.legacyCalls)
	}
	if len(messages) != 1 || messages[0].Content[0].Text != "instruction context" {
		t.Fatalf("messages = %#v", messages)
	}
	if len(categories) != 1 || categories[0] != agent.ContextInstructions {
		t.Fatalf("categories = %#v", categories)
	}
}

func TestInstructionsUsesContentFreeDiscoveryReport(t *testing.T) {
	builder := &instructionContextPrompt{}
	service := &Service{deps: Dependencies{
		Sessions:  instructionSessionRepository{session: Session{ID: "session", WorkspaceID: "workspace", WorktreeID: "worktree"}},
		Prompts:   builder,
		Worktrees: instructionWorktreeReader{worktree: Worktree{ID: "worktree", Root: "repository-root"}},
	}}
	sources, err := service.Instructions(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if builder.instructionCalls != 1 || len(sources) != 1 {
		t.Fatalf("instruction calls=%d sources=%#v", builder.instructionCalls, sources)
	}
	if sources[0].Source != "AGENTS.md" || sources[0].SHA256 != strings.Repeat("a", 64) || sources[0].Status != InstructionLoaded {
		t.Fatalf("instruction source = %#v", sources[0])
	}
}

func TestContextProjectsLatestPreparedRequestAndProviderExactCount(t *testing.T) {
	agentSessions := agentsession.NewMemoryRepository()
	if err := agentSessions.Create(context.Background(), agentsession.Metadata{ID: "agent-session"}); err != nil {
		t.Fatal(err)
	}
	categories := []agentsession.ContextCategoryData{
		{Category: "system", Tokens: 10, Items: 2}, {Category: "task", Tokens: 3, Items: 1},
		{Category: "instructions", Tokens: 4, Items: 1}, {Category: "skills", Tokens: 0, Items: 0},
		{Category: "history", Tokens: 8, Items: 2}, {Category: "tool_results", Tokens: 5, Items: 1},
		{Category: "artifacts", Tokens: 2, Items: 1}, {Category: "reserved_output", Tokens: 20, Items: 1},
	}
	_, err := agentSessions.AppendRecord(context.Background(), "agent-session", agentsession.MainLane, agentsession.Record{
		ID: "context", Type: agentsession.RecordContextPrepared, RunID: "run", Context: &agentsession.ContextData{
			Attempt: 2, CountSource: "local_byte_estimate", Estimated: true, InputTokens: 32,
			ContextWindow: 100, InputBudget: 75, ReservedOutput: 20, SafetyMargin: 5,
			SummarizeThreshold: 60, HardLimit: 75, BudgetSource: "model_metadata", Compacted: true,
			Categories: categories, Degradations: []agentsession.ContextDegradationData{{Kind: "summary_safe_trim", Reason: "Old history was safely trimmed."}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = agentSessions.AppendRecord(context.Background(), "agent-session", agentsession.MainLane, agentsession.Record{
		ID: "usage", Type: agentsession.RecordUsage, RunID: "run", Usage: &llm.Usage{InputTokens: 35, OutputTokens: 5, TotalTokens: 40},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = agentSessions.AppendRecord(context.Background(), "agent-session", agentsession.MainLane, agentsession.Record{
		ID: "step-finished", Type: agentsession.RecordStepFinished, RunID: "run", Step: &agentsession.StepData{Attempt: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = agentSessions.AppendRecord(context.Background(), "agent-session", agentsession.MainLane, agentsession.Record{
		ID: "later-summary-usage", Type: agentsession.RecordUsage, RunID: "run", Usage: &llm.Usage{InputTokens: 999, OutputTokens: 1, TotalTokens: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{deps: Dependencies{
		Sessions:      instructionSessionRepository{session: Session{ID: "session", AgentSessionID: "agent-session"}},
		AgentSessions: agentSessions,
	}}
	report, err := service.Context(context.Background(), "session")
	if err != nil || !report.Available || !report.ProviderExact || report.ProviderInput != 35 || !report.Estimated || len(report.Categories) != 8 || !report.Compacted {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if report.Categories[2].Source != "project instruction context" || report.Degradations[0].Kind != "summary_safe_trim" {
		t.Fatalf("unsafe or incomplete projection = %#v", report)
	}
}

func TestProjectInstructionSourceRejectsUnsafeMetadataAndSanitizesDiagnostics(t *testing.T) {
	unsafePath := projectInstructionSource(InstructionSource{
		Source: "/private/AGENTS.md", Scope: ".", SHA256: strings.Repeat("a", 64), Status: InstructionLoaded,
	})
	if unsafePath.Status != InstructionFailed || unsafePath.Source != "." {
		t.Fatalf("unsafe path projection = %#v", unsafePath)
	}
	invalidDigest := projectInstructionSource(InstructionSource{
		Source: "AGENTS.md", Scope: ".", SHA256: "not-a-digest", Status: InstructionLoaded,
	})
	if invalidDigest.Status != InstructionFailed || invalidDigest.SHA256 != "" {
		t.Fatalf("invalid digest projection = %#v", invalidDigest)
	}
	diagnostic := projectInstructionSource(InstructionSource{
		Source: "AGENT.md", Scope: ".", Status: InstructionIgnored,
		Diagnostic: "fix this\nTOKEN=top-secret-value",
	})
	if strings.ContainsAny(diagnostic.Diagnostic, "\r\n") || strings.Contains(diagnostic.Diagnostic, "top-secret-value") || !strings.Contains(diagnostic.Diagnostic, RedactedValue) {
		t.Fatalf("diagnostic projection = %#v", diagnostic)
	}
}
