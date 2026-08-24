package file

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
)

func TestRepositoryRebuildsSharedSequenceAndLanes(t *testing.T) {
	repository, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	ctx := context.Background()
	if err := repository.Create(ctx, agentsession.Metadata{ID: "session-1"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	first, err := repository.AppendEntry(ctx, "session-1", agentsession.MainLane, messageEntry("entry-1", "run-1", llm.RoleUser, "hello"))
	if err != nil {
		t.Fatalf("append first entry: %v", err)
	}
	if err := repository.ForkLane(ctx, "session-1", "branch", first.ID); err != nil {
		t.Fatalf("fork lane: %v", err)
	}
	branch, err := repository.AppendEntry(ctx, "session-1", "branch", messageEntry("entry-2", "run-2", llm.RoleUser, "branch"))
	if err != nil {
		t.Fatalf("append branch entry: %v", err)
	}
	if branch.ParentID != first.ID || branch.Sequence != 3 {
		t.Fatalf("branch entry = %#v", branch)
	}
	reopened, err := NewRepository(repository.root)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	snapshot, err := reopened.Load(ctx, "session-1")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(snapshot.Log) != 3 || len(snapshot.Lanes) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRepositoryListsArchivesAndRecoversPartialCreation(t *testing.T) {
	repository, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	partial := agentsession.ID("partial-session")
	if err := os.MkdirAll(repository.sessionDirectory(partial), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(ctx, agentsession.Metadata{ID: partial, Name: "recovered"}); err != nil {
		t.Fatalf("recover partial creation: %v", err)
	}
	if err := repository.Create(ctx, agentsession.Metadata{ID: "regular-session"}); err != nil {
		t.Fatal(err)
	}
	values, err := repository.List(ctx)
	if err != nil || len(values) != 2 || values[0].ID != partial {
		t.Fatalf("listed metadata = %#v, %v", values, err)
	}
	if err := repository.SetArchived(ctx, partial, true); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	reopened, err := NewRepository(repository.root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Load(ctx, partial)
	if err != nil || !snapshot.Metadata.Archived {
		t.Fatalf("archived snapshot = %#v, %v", snapshot, err)
	}
}

func TestRepositoryPersistsModelNeutralSummary(t *testing.T) {
	repository, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	key := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	summary := contextmanager.Summary{Text: "summary", SourceDigest: "digest", Strategy: "rolling", StrategyVersion: "v1", Model: llm.ModelRef{Provider: "profile-a", Model: "model-a"}}
	if err := repository.SaveSummary(context.Background(), key, summary); err != nil {
		t.Fatalf("save summary: %v", err)
	}
	loaded, found, err := repository.LoadSummary(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("load summary: found=%v err=%v", found, err)
	}
	if loaded.Text != summary.Text || loaded.Model != summary.Model {
		t.Fatalf("loaded summary = %#v", loaded)
	}
}

func TestJournalArchiveIsImmutableContentAddressedAndLeavesLiveAuditSource(t *testing.T) {
	repository, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	ctx := context.Background()
	if err := repository.Create(ctx, agentsession.Metadata{ID: "session-archive"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := repository.AppendEntry(ctx, "session-archive", agentsession.MainLane, messageEntry("entry-1", "run-1", llm.RoleUser, "original")); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	metadataBefore, _ := os.ReadFile(repository.metadataPath("session-archive"))
	journalBefore, _ := os.ReadFile(repository.journalPath("session-archive"))
	first, err := repository.CreateJournalArchive(ctx, "session-archive")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	second, err := repository.CreateJournalArchive(ctx, "session-archive")
	if err != nil || second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("idempotent archive: first=%#v second=%#v err=%v", first, second, err)
	}
	archives, err := repository.ListJournalArchives(ctx, "session-archive")
	if err != nil || len(archives) != 1 || archives[0].ID != first.ID {
		t.Fatalf("list archives: %#v err=%v", archives, err)
	}
	loaded, err := repository.LoadJournalArchive(ctx, first)
	if err != nil || !bytes.Equal(loaded.Metadata, metadataBefore) || !bytes.Equal(loaded.Journal, journalBefore) {
		t.Fatalf("load exact archive: loaded=%#v err=%v", loaded, err)
	}
	journalAfterArchive, _ := os.ReadFile(repository.journalPath("session-archive"))
	if !bytes.Equal(journalAfterArchive, journalBefore) {
		t.Fatal("creating an archive mutated the live journal")
	}
	if _, err := repository.AppendEntry(ctx, "session-archive", agentsession.MainLane, messageEntry("entry-2", "run-2", llm.RoleUser, "later")); err != nil {
		t.Fatalf("append after archive: %v", err)
	}
	snapshot, err := repository.Load(ctx, "session-archive")
	if err != nil || len(snapshot.Log) != 2 {
		t.Fatalf("live recovery after archive: snapshot=%#v err=%v", snapshot, err)
	}
	old, err := repository.LoadJournalArchive(ctx, first)
	if err != nil || !bytes.Equal(old.Journal, journalBefore) {
		t.Fatalf("old archive changed: %#v err=%v", old, err)
	}
}

func TestRepositoryIgnoresAndRepairsTruncatedTail(t *testing.T) {
	repository, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	ctx := context.Background()
	if err := repository.Create(ctx, agentsession.Metadata{ID: "session-1"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := repository.AppendEntry(ctx, "session-1", agentsession.MainLane, messageEntry("entry-1", "run-1", llm.RoleUser, "hello")); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	file, err := os.OpenFile(repository.journalPath("session-1"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if _, err := file.WriteString(`{"version":1,"sequence":2`); err != nil {
		t.Fatalf("write truncated tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}
	snapshot, err := repository.Load(ctx, "session-1")
	if err != nil {
		t.Fatalf("load recovered session: %v", err)
	}
	if len(snapshot.Warnings) != 1 || len(snapshot.Log) != 1 {
		t.Fatalf("recovery snapshot = %#v", snapshot)
	}
	second, err := repository.AppendEntry(ctx, "session-1", agentsession.MainLane, messageEntry("entry-2", "run-2", llm.RoleUser, "again"))
	if err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	if second.Sequence != 2 {
		t.Fatalf("second sequence = %d", second.Sequence)
	}
}

func TestRepositoryRecoversSubprocessCrashDuringJournalAppend(t *testing.T) {
	root := t.TempDir()
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	ctx := context.Background()
	const sessionID = agentsession.ID("session-subprocess-crash")
	if err := repository.Create(ctx, agentsession.Metadata{ID: sessionID}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := repository.AppendEntry(ctx, sessionID, agentsession.MainLane, messageEntry("entry-before-crash", "run-before-crash", llm.RoleUser, "before")); err != nil {
		t.Fatalf("append entry before crash: %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestJournalCrashHelperProcess$")
	command.Env = append(os.Environ(), "CODEPILOT_JOURNAL_CRASH_HELPER=1", "CODEPILOT_JOURNAL_CRASH_ROOT="+root, "CODEPILOT_JOURNAL_CRASH_SESSION="+string(sessionID))
	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 73 {
		t.Fatalf("journal crash helper error = %v, want exit 73", err)
	}

	reopened, err := OpenRepository(root)
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	snapshot, err := reopened.Load(ctx, sessionID)
	if err != nil {
		t.Fatalf("load after crash: %v", err)
	}
	if len(snapshot.Log) != 1 || len(snapshot.Warnings) != 1 || snapshot.Warnings[0].Kind != "truncated_journal_tail" {
		t.Fatalf("post-crash snapshot = %#v", snapshot)
	}
	second, err := reopened.AppendEntry(ctx, sessionID, agentsession.MainLane, messageEntry("entry-after-crash", "run-after-crash", llm.RoleUser, "after"))
	if err != nil {
		t.Fatalf("append after subprocess crash: %v", err)
	}
	if second.Sequence != 2 {
		t.Fatalf("post-crash sequence = %d, want 2", second.Sequence)
	}
	repaired, err := reopened.Load(ctx, sessionID)
	if err != nil || len(repaired.Log) != 2 || len(repaired.Warnings) != 0 {
		t.Fatalf("repaired snapshot = %#v, err=%v", repaired, err)
	}
}

func TestJournalCrashHelperProcess(t *testing.T) {
	if os.Getenv("CODEPILOT_JOURNAL_CRASH_HELPER") != "1" {
		return
	}
	repository, err := OpenRepository(os.Getenv("CODEPILOT_JOURNAL_CRASH_ROOT"))
	if err != nil {
		os.Exit(71)
	}
	path := repository.journalPath(agentsession.ID(os.Getenv("CODEPILOT_JOURNAL_CRASH_SESSION")))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(72)
	}
	_, _ = file.WriteString(`{"version":1,"sequence":2,"entry":{"id":"entry-crash"`)
	_ = file.Sync()
	os.Exit(73)
}

func messageEntry(id agentsession.EntryID, runID agentsession.RunID, role llm.Role, text string) agentsession.Entry {
	return agentsession.Entry{ID: id, RunID: runID, Type: agentsession.EntryMessage, Message: &llm.Message{Role: role, Content: []llm.Content{{Type: llm.ContentText, Text: text}}}}
}
