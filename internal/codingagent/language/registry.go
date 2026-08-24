package language

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// Registry freezes an explicit strategy set and returns every detected language.
type Registry struct{ strategies []Strategy }

// NewRegistry validates and freezes strategies.
func NewRegistry(strategies ...Strategy) (*Registry, error) {
	seen := make(map[ID]struct{}, len(strategies))
	values := make([]Strategy, 0, len(strategies))
	for _, strategy := range strategies {
		if nilStrategy(strategy) {
			return nil, errors.New("create language registry: strategy is required")
		}
		id := strategy.ID()
		if id != Go && id != Python && id != Node {
			return nil, fmt.Errorf("create language registry: unsupported strategy %q", id)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("create language registry: duplicate strategy %q", id)
		}
		seen[id] = struct{}{}
		values = append(values, strategy)
	}
	return &Registry{strategies: values}, nil
}

// NewDefaultRegistry supports Go, Python and Node/TypeScript worktrees.
func NewDefaultRegistry() *Registry {
	registry, _ := NewRegistry(GoStrategy{}, PythonStrategy{}, NodeStrategy{})
	return registry
}

// Detect reads only bounded root metadata and returns deterministic profiles.
func (r *Registry) Detect(ctx context.Context, root string) (WorkspaceProfile, error) {
	if r == nil {
		return WorkspaceProfile{}, errors.New("detect worktree languages: registry is nil")
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceProfile{}, err
	}
	root = filepath.Clean(strings.TrimSpace(root))
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return WorkspaceProfile{}, errors.New("detect worktree languages: root is unavailable")
	}
	result := WorkspaceProfile{}
	for _, strategy := range r.strategies {
		detection, err := strategy.Detect(ctx, root)
		if err != nil {
			return WorkspaceProfile{}, fmt.Errorf("detect language %q: %w", strategy.ID(), err)
		}
		if detection.Score <= 0 {
			continue
		}
		profile := strategy.Profile()
		if err := validateProfile(profile, strategy.ID()); err != nil {
			return WorkspaceProfile{}, err
		}
		profile.Score = detection.Score
		profile.Evidence = boundedEvidence(detection.Evidence)
		result.Languages = append(result.Languages, cloneProfile(profile))
	}
	sort.Slice(result.Languages, func(left, right int) bool {
		if result.Languages[left].Score != result.Languages[right].Score {
			return result.Languages[left].Score > result.Languages[right].Score
		}
		return result.Languages[left].ID < result.Languages[right].ID
	})
	return result, nil
}

func validateProfile(profile Profile, expected ID) error {
	if profile.ID != expected || strings.TrimSpace(profile.PromptHint) == "" || strings.TrimSpace(profile.Server.Program) == "" || len(profile.Extensions) == 0 || len(profile.Server.Args) > 8 {
		return fmt.Errorf("build language profile %q: invalid strategy profile", expected)
	}
	if err := ValidateServer(profile); err != nil {
		return fmt.Errorf("build language profile %q: %w", expected, err)
	}
	return nil
}

func cloneProfile(profile Profile) Profile {
	profile.Evidence = append([]string(nil), profile.Evidence...)
	profile.Extensions = append([]string(nil), profile.Extensions...)
	profile.Server.Args = append([]string(nil), profile.Server.Args...)
	return profile
}

func boundedEvidence(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, min(len(values), 16))
	for _, value := range values {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == 16 {
			break
		}
	}
	sort.Strings(result)
	return result
}

func nilStrategy(value Strategy) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func extension(relative string) string { return strings.ToLower(filepath.Ext(relative)) }
func hasExtension(relative, expected string) bool {
	return extension(relative) == strings.ToLower(expected)
}
