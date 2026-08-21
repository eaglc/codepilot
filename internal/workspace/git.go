package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
)

type gitCommandResult struct {
	stdout          []byte
	stderr          []byte
	exitCode        int
	truncated       bool
	stdoutTruncated bool
	stderrTruncated bool
}

// ReadWorktreeState returns the current branch, HEAD, and dirty flag.
func (s *Service) ReadWorktreeState(ctx context.Context, root string) (session.WorktreeState, error) {
	status, err := s.readStatus(ctx, root)
	if err != nil {
		return session.WorktreeState{}, err
	}
	canonicalRoot, err := s.verifiedWorktreeRoot(ctx, root)
	if err != nil {
		return session.WorktreeState{}, err
	}
	return session.WorktreeState{
		Root:       canonicalRoot,
		Branch:     status.Branch,
		HeadCommit: status.HeadCommit,
		Dirty:      status.Dirty,
		Available:  true,
	}, nil
}

// GitStatus returns bounded non-sensitive status entries.
func (s *Service) GitStatus(ctx context.Context, request agent.GitStatusRequest) (agent.GitStatusResult, error) {
	return s.readStatus(ctx, request.WorktreeRoot)
}

func runGit(ctx context.Context, directory string, maxBytes int, allowedExitCodes []int, arguments ...string) (gitCommandResult, error) {
	return runGitInput(ctx, directory, maxBytes, allowedExitCodes, nil, arguments...)
}

func runGitInput(ctx context.Context, directory string, maxBytes int, allowedExitCodes []int, input []byte, arguments ...string) (gitCommandResult, error) {
	if err := ctx.Err(); err != nil {
		return gitCommandResult{}, err
	}
	commandArguments := append([]string{"--no-optional-locks", "-C", directory}, arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	command.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_LITERAL_PATHSPECS=1",
		"LC_ALL=C",
	)
	stdout := newBoundedBuffer(maxBytes)
	stderr := newBoundedBuffer(min(maxBytes, 64<<10))
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result := gitCommandResult{
		stdout:          append([]byte(nil), stdout.Bytes()...),
		stderr:          append([]byte(nil), stderr.Bytes()...),
		exitCode:        0,
		truncated:       stdout.truncated || stderr.truncated,
		stdoutTruncated: stdout.truncated,
		stderrTruncated: stderr.truncated,
	}
	if err == nil {
		return result, nil
	}
	if contextError := ctx.Err(); contextError != nil {
		return gitCommandResult{}, contextError
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.exitCode = exitError.ExitCode()
		for _, allowed := range allowedExitCodes {
			if result.exitCode == allowed {
				return result, nil
			}
		}
	}
	return gitCommandResult{}, fmt.Errorf("run git %s: %w", strings.Join(arguments, " "), err)
}

func gitLine(ctx context.Context, directory string, maxBytes int, allowedExitCodes []int, arguments ...string) (string, int, error) {
	result, err := runGit(ctx, directory, maxBytes, allowedExitCodes, arguments...)
	if err != nil {
		return "", 0, err
	}
	if result.truncated {
		return "", result.exitCode, errors.New("git output exceeded its limit")
	}
	return strings.TrimSpace(string(result.stdout)), result.exitCode, nil
}

func (s *Service) readStatus(ctx context.Context, root string) (agent.GitStatusResult, error) {
	canonicalRoot, err := s.verifiedWorktreeRoot(ctx, root)
	if err != nil {
		return agent.GitStatusResult{}, err
	}
	branch, _, err := gitLine(ctx, canonicalRoot, s.limits.MaxGitOutputBytes, []int{0, 1}, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return agent.GitStatusResult{}, wrapGitError("workspace.git_branch", err)
	}
	head, exitCode, err := gitLine(ctx, canonicalRoot, s.limits.MaxGitOutputBytes, []int{0, 128}, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return agent.GitStatusResult{}, wrapGitError("workspace.git_head", err)
	}
	if exitCode != 0 {
		head = ""
	}
	status, err := runGit(ctx, canonicalRoot, s.limits.MaxGitOutputBytes, []int{0}, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return agent.GitStatusResult{}, wrapGitError("workspace.git_status", err)
	}
	entries, hidden, entriesTruncated := s.parseStatusEntries(canonicalRoot, status.stdout, status.stdoutTruncated)
	return agent.GitStatusResult{
		Branch:        branch,
		HeadCommit:    head,
		Entries:       entries,
		Dirty:         len(status.stdout) > 0,
		HiddenEntries: hidden,
		Truncated:     status.truncated || entriesTruncated,
	}, nil
}

func (s *Service) parseStatusEntries(root string, output []byte, outputTruncated bool) ([]agent.GitStatusEntry, int, bool) {
	items := splitNULRecords(output, outputTruncated)
	entries := make([]agent.GitStatusEntry, 0, min(len(items), s.limits.MaxFiles))
	hidden := 0
	truncated := false
	for index := 0; index < len(items); index++ {
		item := items[index]
		if len(item) < 4 {
			continue
		}
		status := string(item[:2])
		pathValue := string(item[3:])
		var pairedPath string
		if status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C' {
			index++
			if index < len(items) {
				pairedPath = string(items[index])
			}
		}
		_, relative, err := securePath(root, pathValue, true)
		if err != nil {
			hidden++
			continue
		}
		if pairedPath != "" {
			if _, _, err := securePath(root, pairedPath, true); err != nil {
				hidden++
				continue
			}
		}
		if len(entries) == s.limits.MaxFiles {
			truncated = true
			continue
		}
		entries = append(entries, agent.GitStatusEntry{Path: relative, Status: status})
	}
	return entries, hidden, truncated
}

func splitNULRecords(output []byte, truncated bool) [][]byte {
	items := bytes.Split(output, []byte{0})
	if truncated && len(output) > 0 && output[len(output)-1] != 0 && len(items) > 0 {
		items = items[:len(items)-1]
	}
	return items
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: max(limit, 1)}
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || originalLength > 0
		return originalLength, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(value)
	return originalLength, nil
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func parseCount(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}
