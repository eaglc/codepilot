// Package lsp provides worktree-isolated, bounded language-server navigation.
package lsp

import (
	"context"
	"errors"

	"github.com/eaglc/codepilot/internal/codingagent/language"
)

// ErrUnavailable reports a missing, crashed, or unsupported language server.
var ErrUnavailable = errors.New("language navigation is unavailable")

// Scope is the immutable worktree/language binding for one query.
type Scope struct {
	WorktreeID string
	Root       string
	Language   language.Profile
}

// Position is a one-based UTF-16 LSP source position at the product boundary.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Range is a bounded source range.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is an in-worktree navigation result.
type Location struct {
	Path  string `json:"path"`
	Range Range  `json:"range"`
}

// Diagnostic is a product-neutral LSP diagnostic.
type Diagnostic struct {
	Path     string `json:"path"`
	Range    Range  `json:"range"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
	Code     string `json:"code,omitempty"`
}

// Symbol is a document symbol with an optional container name.
type Symbol struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Container string   `json:"container,omitempty"`
	Location  Location `json:"location"`
}

// Navigator is the read-only capability consumed by Coding tools.
type Navigator interface {
	Ready(scope Scope) bool
	Definition(ctx context.Context, scope Scope, path string, position Position, limit int) ([]Location, error)
	References(ctx context.Context, scope Scope, path string, position Position, includeDeclaration bool, limit int) ([]Location, error)
	Diagnostics(ctx context.Context, scope Scope, path string, limit int) ([]Diagnostic, error)
	DocumentSymbols(ctx context.Context, scope Scope, path string, limit int) ([]Symbol, error)
	CloseWorktree(ctx context.Context, worktreeID string) error
	Close() error
}
