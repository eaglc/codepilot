// Package language detects supported worktree languages without executing
// project code and supplies immutable language-server profiles.
package language

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
)

// ID is a stable product language identifier.
type ID string

const (
	Go     ID = "go"
	Python ID = "python"
	Node   ID = "node"
)

// Detection is bounded local evidence from one read-only strategy.
type Detection struct {
	Score    int
	Evidence []string
}

// Server describes one allowlisted stdio language-server command.
type Server struct {
	Program string
	Args    []string
}

// ValidateServer verifies that a language profile names one exact executable
// basename and argument list from CodePilot's trusted server allowlist.
func ValidateServer(profile Profile) error {
	program := strings.TrimSuffix(strings.ToLower(filepath.Base(profile.Server.Program)), ".exe")
	valid := profile.ID == Go && program == "gopls" && reflect.DeepEqual(profile.Server.Args, []string{"serve"})
	valid = valid || profile.ID == Python && (program == "pyright-langserver" || program == "basedpyright-langserver") && reflect.DeepEqual(profile.Server.Args, []string{"--stdio"})
	valid = valid || profile.ID == Node && program == "typescript-language-server" && reflect.DeepEqual(profile.Server.Args, []string{"--stdio"})
	if !valid || filepath.Base(profile.Server.Program) != profile.Server.Program {
		return errors.New("language server is outside the allowlist")
	}
	return nil
}

// ServerBinding returns the stable process identity for an allowlisted profile.
func ServerBinding(profile Profile) string {
	return profile.Server.Program + "\x00" + strings.Join(profile.Server.Args, "\x00")
}

// Profile is the immutable detected language capability used by Coding tools.
type Profile struct {
	ID         ID
	Score      int
	Evidence   []string
	Extensions []string
	PromptHint string
	Server     Server
}

// WorkspaceProfile can represent polyglot repositories deterministically.
type WorkspaceProfile struct {
	Languages []Profile
}

// Strategy detects one language using filesystem metadata only.
type Strategy interface {
	ID() ID
	Detect(ctx context.Context, root string) (Detection, error)
	Profile() Profile
}

// ResolvePath chooses a detected language by normalized file extension.
func (p WorkspaceProfile) ResolvePath(relative string) (Profile, bool) {
	for _, profile := range p.Languages {
		for _, extension := range profile.Extensions {
			if hasExtension(relative, extension) {
				return cloneProfile(profile), true
			}
		}
	}
	return Profile{}, false
}

// DocumentLanguage returns the LSP languageId for a supported path.
func DocumentLanguage(id ID, relative string) string {
	switch id {
	case Go:
		return "go"
	case Python:
		return "python"
	case Node:
		switch extension(relative) {
		case ".ts":
			return "typescript"
		case ".tsx":
			return "typescriptreact"
		case ".jsx":
			return "javascriptreact"
		default:
			return "javascript"
		}
	default:
		return ""
	}
}
