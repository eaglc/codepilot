package language

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Registry resolves an ordered set of language strategies. Equal top scores
// deliberately fall back to Generic so registration order never changes the
// selected command plans.
type Registry struct {
	strategies []Strategy
}

// NewRegistry validates and freezes the supplied language strategies.
func NewRegistry(strategies ...Strategy) (*Registry, error) {
	values := make([]Strategy, 0, len(strategies))
	seen := make(map[LanguageID]struct{}, len(strategies))
	for _, strategy := range strategies {
		if isNilStrategy(strategy) {
			return nil, errors.New("create language registry: strategy is required")
		}
		id := strategy.ID()
		if !validLanguageID(id) || id == LanguageGeneric {
			return nil, fmt.Errorf("create language registry: strategy ID %q is invalid", id)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("create language registry: duplicate strategy %q", id)
		}
		seen[id] = struct{}{}
		values = append(values, strategy)
	}
	return &Registry{strategies: values}, nil
}

// ResolveLanguage chooses the only highest-scoring strategy and returns a
// Generic profile when no strategy matches or the best score is tied.
func (r *Registry) ResolveLanguage(ctx context.Context, root string) (LanguageProfile, error) {
	if err := ctx.Err(); err != nil {
		return LanguageProfile{}, err
	}
	if r == nil {
		return LanguageProfile{}, errors.New("resolve language: registry is nil")
	}
	if strings.TrimSpace(root) == "" {
		return LanguageProfile{}, errors.New("resolve language: worktree root is required")
	}

	bestScore := 0
	bestIndex := -1
	tied := false
	for index, strategy := range r.strategies {
		detection, err := strategy.Detect(ctx, root)
		if err != nil {
			return LanguageProfile{}, fmt.Errorf("resolve language %q: %w", strategy.ID(), err)
		}
		if detection.Score <= 0 || detection.Score < bestScore {
			continue
		}
		if detection.Score == bestScore {
			tied = true
			continue
		}
		bestScore = detection.Score
		bestIndex = index
		tied = false
	}
	if bestIndex < 0 || tied {
		return genericProfile(), nil
	}

	profile, err := r.strategies[bestIndex].BuildProfile(ctx, root)
	if err != nil {
		return LanguageProfile{}, fmt.Errorf("build language profile %q: %w", r.strategies[bestIndex].ID(), err)
	}
	if err := validateProfile(profile, r.strategies[bestIndex].ID()); err != nil {
		return LanguageProfile{}, err
	}
	return cloneProfile(profile), nil
}

func genericProfile() LanguageProfile {
	return LanguageProfile{
		ID:         LanguageGeneric,
		PromptHint: "No supported project language was detected. Inspect project metadata before editing; no trusted project check plan is available for this turn.",
	}
}

func validateProfile(profile LanguageProfile, expected LanguageID) error {
	if profile.ID != expected || strings.TrimSpace(profile.PromptHint) == "" {
		return fmt.Errorf("build language profile %q: strategy returned an invalid profile", expected)
	}
	seen := make(map[string]struct{}, len(profile.CheckPlans))
	for _, plan := range profile.CheckPlans {
		if strings.TrimSpace(plan.ID) == "" || plan.Command.ID != plan.ID || strings.TrimSpace(plan.Description) == "" || strings.TrimSpace(plan.Command.Program) == "" || plan.Command.Timeout <= 0 || plan.Command.MaxOutputBytes <= 0 {
			return fmt.Errorf("build language profile %q: check plan %q is invalid", expected, plan.ID)
		}
		if _, exists := seen[plan.ID]; exists {
			return fmt.Errorf("build language profile %q: duplicate check plan %q", expected, plan.ID)
		}
		seen[plan.ID] = struct{}{}
	}
	return nil
}

func cloneProfile(profile LanguageProfile) LanguageProfile {
	profile.CheckPlans = append([]CheckPlan(nil), profile.CheckPlans...)
	for index := range profile.CheckPlans {
		profile.CheckPlans[index].Command.Args = append([]string(nil), profile.CheckPlans[index].Command.Args...)
		profile.CheckPlans[index].Command.EnvAllowlist = append([]string(nil), profile.CheckPlans[index].Command.EnvAllowlist...)
	}
	return profile
}

func validLanguageID(id LanguageID) bool {
	value := string(id)
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func isNilStrategy(value Strategy) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}
