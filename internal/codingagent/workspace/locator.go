// Package workspace owns Coding Agent workspace and worktree discovery.
package workspace

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxGitMetadataBytes = 128 << 10

// ResolvedWorktree contains normalized Git paths discovered from a user-selected directory.
type ResolvedWorktree struct {
	Root                  string
	GitDir                string
	GitCommonDir          string
	RepositoryFingerprint string
}

// ResolveWorktree resolves and validates one Git worktree without mutating it.
func ResolveWorktree(ctx context.Context, path string) (ResolvedWorktree, error) {
	if err := ctx.Err(); err != nil {
		return ResolvedWorktree{}, err
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = resolveCtx
	if strings.TrimSpace(path) == "" {
		return ResolvedWorktree{}, errors.New("resolve worktree: path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ResolvedWorktree{}, fmt.Errorf("resolve worktree: absolute path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	root, err := gitPath(ctx, absolute, "--show-toplevel")
	if err != nil {
		return ResolvedWorktree{}, fmt.Errorf("resolve worktree: %w", err)
	}
	gitDir, err := gitPath(ctx, absolute, "--git-dir")
	if err != nil {
		return ResolvedWorktree{}, fmt.Errorf("resolve worktree git directory: %w", err)
	}
	commonDir, err := gitPath(ctx, absolute, "--git-common-dir")
	if err != nil {
		return ResolvedWorktree{}, fmt.Errorf("resolve worktree common directory: %w", err)
	}
	root = resolveGitReportedPath(absolute, root)
	fingerprint, err := repositoryFingerprint(ctx, root)
	if err != nil {
		return ResolvedWorktree{}, err
	}
	return ResolvedWorktree{Root: root, GitDir: resolveGitReportedPath(absolute, gitDir), GitCommonDir: resolveGitReportedPath(absolute, commonDir), RepositoryFingerprint: fingerprint}, nil
}

func gitPath(ctx context.Context, directory string, argument string) (string, error) {
	output, err := gitOutput(ctx, directory, "rev-parse", argument)
	if err != nil {
		return "", errors.New("the selected directory is not an available Git worktree")
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", errors.New("Git returned an empty worktree path")
	}
	return value, nil
}

func repositoryFingerprint(ctx context.Context, root string) (string, error) {
	objectFormat, err := gitOutput(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return "", errors.New("resolve worktree: Git object format is unavailable")
	}
	format := strings.TrimSpace(string(objectFormat))
	if format != "sha1" && format != "sha256" {
		return "", errors.New("resolve worktree: Git object format is unsupported")
	}
	output, err := gitOutput(ctx, root, "rev-list", "--max-parents=0", "--first-parent", "HEAD")
	if err != nil {
		// Unborn HEAD means an empty repository, which intentionally has no
		// relocation identity.
		return "", nil
	}
	lines := strings.Fields(string(output))
	if len(lines) == 0 {
		// An empty repository has no content-derived identity. Keeping this empty
		// prevents automatic or explicit relocation from confusing two unrelated
		// empty repositories.
		return "", nil
	}
	if len(lines) != 1 {
		return "", errors.New("resolve worktree: Git history identity is ambiguous")
	}
	expectedLength := 40
	if format == "sha256" {
		expectedLength = 64
	}
	anchor := lines[0]
	if len(anchor) != expectedLength {
		return "", errors.New("resolve worktree: Git history identity is invalid")
	}
	if _, err := hex.DecodeString(anchor); err != nil {
		return "", errors.New("resolve worktree: Git history identity is invalid")
	}
	return "git-anchor-v1:" + format + ":" + anchor, nil
}

// VerifyRepositoryFingerprint checks that the candidate history still contains
// the registration anchor. New commits, branches and merges therefore do not
// change workspace identity, while an amended or rebased history is rejected
// even when Git has retained the old commit object in its local database.
func VerifyRepositoryFingerprint(ctx context.Context, root, fingerprint string) error {
	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx = verifyCtx
	format, anchor, err := parseRepositoryFingerprint(fingerprint)
	if err != nil {
		return err
	}
	actualFormat, err := gitOutput(ctx, root, "rev-parse", "--show-object-format")
	if err != nil || strings.TrimSpace(string(actualFormat)) != format {
		return errors.New("Git object format does not match the stored workspace identity")
	}
	if _, err := gitOutput(ctx, root, "cat-file", "-e", anchor+"^{commit}"); err != nil {
		return errors.New("Git history does not contain the stored workspace identity")
	}
	currentRoot, err := gitOutput(ctx, root, "rev-list", "--max-parents=0", "--first-parent", "HEAD")
	if err != nil || strings.TrimSpace(string(currentRoot)) != anchor {
		return errors.New("Git history no longer contains the stored workspace identity")
	}
	if _, err := gitOutput(ctx, root, "merge-base", "--is-ancestor", anchor, "HEAD"); err != nil {
		return errors.New("Git history no longer contains the stored workspace identity")
	}
	return nil
}

func parseRepositoryFingerprint(value string) (string, string, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "git-anchor-v1" || (parts[1] != "sha1" && parts[1] != "sha256") {
		return "", "", errors.New("stored workspace identity is invalid")
	}
	expectedLength := 40
	if parts[1] == "sha256" {
		expectedLength = 64
	}
	if len(parts[2]) != expectedLength {
		return "", "", errors.New("stored workspace identity is invalid")
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return "", "", errors.New("stored workspace identity is invalid")
	}
	return parts[1], parts[2], nil
}

func gitOutput(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	commandArguments := append([]string{"-C", directory}, arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	command.Stderr = io.Discard
	output := &boundedBuffer{remaining: maxGitMetadataBytes}
	command.Stdout = output
	if err := command.Run(); err != nil {
		return nil, err
	}
	if output.exceeded {
		return nil, errors.New("Git metadata output exceeded the safe limit")
	}
	return append([]byte(nil), output.Bytes()...), nil
}

type boundedBuffer struct {
	bytes.Buffer
	remaining int
	exceeded  bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if len(value) > b.remaining {
		value = value[:max(0, b.remaining)]
		b.exceeded = true
	}
	if len(value) != 0 {
		_, _ = b.Buffer.Write(value)
		b.remaining -= len(value)
	}
	return original, nil
}

func resolveGitReportedPath(base string, value string) string {
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return filepath.Clean(value)
	}
	return CanonicalPath(absolute)
}
