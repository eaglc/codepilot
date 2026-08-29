package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/eaglc/codepilot/internal/codingagent"
)

var planVersionFile = regexp.MustCompile(`^v([0-9]{4,20})\.json$`)

// CreatePlanVersion writes one immutable, sequential Plan version atomically.
func (r *Repository) CreatePlanVersion(ctx context.Context, value codingagent.Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidatePlan(value); err != nil {
		return fmt.Errorf("create Coding plan version: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	turns, err := r.loadTurnsLocked(ctx, "")
	if err != nil {
		return fmt.Errorf("create Coding plan %q: %w", value.ID, err)
	}
	turn, found := turns[value.TurnID]
	if !found {
		return fmt.Errorf("create Coding plan %q: turn %q not found", value.ID, value.TurnID)
	}
	if turn.PlanID != "" && turn.PlanID != value.ID {
		return fmt.Errorf("create Coding plan %q: Product Turn references another Plan", value.ID)
	}
	versions, err := r.listPlanVersionsLocked(ctx, value.ID)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		if value.Version != 1 {
			return fmt.Errorf("create Coding plan %q: initial version must be 1", value.ID)
		}
	} else {
		latest := versions[len(versions)-1]
		if latest.TurnID != value.TurnID || value.Version != latest.Version+1 {
			return fmt.Errorf("create Coding plan %q: version or immutable Turn binding is invalid", value.ID)
		}
	}
	path, err := r.planVersionPath(value.ID, value.Version)
	if err != nil {
		return err
	}
	if _, found, readErr := readEnvelope[codingagent.Plan](path); readErr != nil {
		return fmt.Errorf("create Coding plan %q version %d: %w", value.ID, value.Version, readErr)
	} else if found {
		return fmt.Errorf("create Coding plan %q version %d: already exists", value.ID, value.Version)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Coding plan %q directory: %w", value.ID, err)
	}
	return writeEnvelope(path, value)
}

// LoadPlan loads one exact immutable Plan revision.
func (r *Repository) LoadPlan(ctx context.Context, id codingagent.PlanID, version uint64) (codingagent.Plan, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Plan{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	path, err := r.planVersionPath(id, version)
	if err != nil {
		return codingagent.Plan{}, err
	}
	value, found, err := readEnvelope[codingagent.Plan](path)
	if err != nil {
		return codingagent.Plan{}, fmt.Errorf("load Coding plan %q version %d: %w", id, version, err)
	}
	if !found {
		return codingagent.Plan{}, fmt.Errorf("load Coding plan %q version %d: %w", id, version, codingagent.ErrPlanNotFound)
	}
	value = codingagent.ApplyPlanCompatibilityDefaults(value)
	if err := codingagent.ValidatePlan(value); err != nil {
		return codingagent.Plan{}, fmt.Errorf("load Coding plan %q version %d: %w", id, version, err)
	}
	return value, nil
}

// ListPlanVersions returns immutable Plan revisions in ascending version order.
func (r *Repository) ListPlanVersions(ctx context.Context, id codingagent.PlanID) ([]codingagent.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listPlanVersionsLocked(ctx, id)
}

func (r *Repository) listPlanVersionsLocked(ctx context.Context, id codingagent.PlanID) ([]codingagent.Plan, error) {
	if id == "" || !validID.MatchString(string(id)) {
		return nil, errors.New("Coding plan id is invalid")
	}
	directory := filepath.Join(r.root, "coding-plans", string(id))
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list Coding plan %q versions: %w", id, err)
	}
	values := make([]codingagent.Plan, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !planVersionFile.MatchString(entry.Name()) {
			continue
		}
		value, found, readErr := readEnvelope[codingagent.Plan](filepath.Join(directory, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("list Coding plan %q versions: %w", id, readErr)
		}
		if !found {
			continue
		}
		value = codingagent.ApplyPlanCompatibilityDefaults(value)
		if value.ID != id {
			return nil, fmt.Errorf("list Coding plan %q versions: stored identity mismatch", id)
		}
		if err := codingagent.ValidatePlan(value); err != nil {
			return nil, fmt.Errorf("list Coding plan %q versions: %w", id, err)
		}
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].Version < values[right].Version })
	for index := range values {
		if values[index].Version != uint64(index+1) {
			return nil, fmt.Errorf("list Coding plan %q versions: revision sequence is incomplete", id)
		}
	}
	return values, nil
}

func (r *Repository) planVersionPath(id codingagent.PlanID, version uint64) (string, error) {
	if id == "" || !validID.MatchString(string(id)) || version == 0 {
		return "", errors.New("Coding plan id and version are invalid")
	}
	return filepath.Join(r.root, "coding-plans", string(id), fmt.Sprintf("v%04d.json", version)), nil
}
