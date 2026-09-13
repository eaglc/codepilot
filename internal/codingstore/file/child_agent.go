package file

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const childAgentDirectory = "coding-child-agents"

// CreateChildAgent writes the durable creation intent before the generic Agent
// session is created. Exact retries are idempotent across crash recovery.
func (r *Repository) CreateChildAgent(ctx context.Context, value codingagent.ChildAgent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateChildAgent(value); err != nil {
		return fmt.Errorf("create Coding child Agent: %w", err)
	}
	if value.Status != codingagent.ChildAgentCreating || value.Revision != 1 {
		return errors.New("create Coding child Agent: initial state must be creating revision 1")
	}
	path, err := r.path(childAgentDirectory, string(value.ID))
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, found, err := readEnvelope[codingagent.ChildAgent](path)
	if err != nil {
		return fmt.Errorf("create Coding child Agent %q: inspect existing value: %w", value.ID, err)
	}
	if found {
		if reflect.DeepEqual(current, value) {
			return nil
		}
		return fmt.Errorf("create Coding child Agent %q: identity already exists with different data", value.ID)
	}
	if err := writeEnvelope(path, codingagent.CloneChildAgent(value)); err != nil {
		return fmt.Errorf("create Coding child Agent %q: %w", value.ID, err)
	}
	return nil
}

// LoadChildAgent reads and validates one durable child projection.
func (r *Repository) LoadChildAgent(ctx context.Context, id codingagent.ChildAgentID) (codingagent.ChildAgent, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.ChildAgent{}, err
	}
	path, err := r.path(childAgentDirectory, string(id))
	if err != nil {
		return codingagent.ChildAgent{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, found, err := readEnvelope[codingagent.ChildAgent](path)
	if err != nil {
		return codingagent.ChildAgent{}, fmt.Errorf("load Coding child Agent %q: %w", id, err)
	}
	if !found {
		return codingagent.ChildAgent{}, fmt.Errorf("load Coding child Agent %q: %w", id, codingagent.ErrChildAgentNotFound)
	}
	if err := codingagent.ValidateChildAgent(value); err != nil {
		return codingagent.ChildAgent{}, fmt.Errorf("load Coding child Agent %q: %w", id, err)
	}
	return codingagent.CloneChildAgent(value), nil
}

// ListChildAgents returns stable creation ordering for one parent Turn.
func (r *Repository) ListChildAgents(ctx context.Context, turnID codingagent.TurnID) ([]codingagent.ChildAgent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values, err := listEnvelopes[codingagent.ChildAgent](ctx, filepath.Join(r.root, childAgentDirectory))
	if err != nil {
		return nil, fmt.Errorf("list Coding child Agents: %w", err)
	}
	filtered := make([]codingagent.ChildAgent, 0, len(values))
	for _, value := range values {
		if err := codingagent.ValidateChildAgent(value); err != nil {
			return nil, fmt.Errorf("list Coding child Agent %q: %w", value.ID, err)
		}
		if turnID == "" || value.ParentTurnID == turnID {
			filtered = append(filtered, codingagent.CloneChildAgent(value))
		}
	}
	sort.Slice(filtered, func(left, right int) bool {
		if filtered[left].CreatedAt.Equal(filtered[right].CreatedAt) {
			return filtered[left].ID < filtered[right].ID
		}
		return filtered[left].CreatedAt.Before(filtered[right].CreatedAt)
	})
	return filtered, nil
}

// SaveChildAgent applies one lifecycle transition with revision CAS.
func (r *Repository) SaveChildAgent(ctx context.Context, value codingagent.ChildAgent, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := r.path(childAgentDirectory, string(value.ID))
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, found, err := readEnvelope[codingagent.ChildAgent](path)
	if err != nil {
		return fmt.Errorf("save Coding child Agent %q: %w", value.ID, err)
	}
	if !found {
		return fmt.Errorf("save Coding child Agent %q: %w", value.ID, codingagent.ErrChildAgentNotFound)
	}
	if current.Revision != expectedRevision {
		return fmt.Errorf("save Coding child Agent %q: expected revision %d, found %d: %w", value.ID, expectedRevision, current.Revision, codingagent.ErrChildAgentConflict)
	}
	if err := codingagent.ValidateChildAgentTransition(current, value); err != nil {
		return fmt.Errorf("save Coding child Agent %q: %w", value.ID, err)
	}
	if err := writeEnvelope(path, codingagent.CloneChildAgent(value)); err != nil {
		return fmt.Errorf("save Coding child Agent %q: %w", value.ID, err)
	}
	return nil
}
