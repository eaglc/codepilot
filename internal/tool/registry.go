package tool

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/eaglc/codepilot/internal/llm"
)

// Registry is an immutable validated set of tools available to one Agent run.
type Registry struct {
	tools map[string]Tool
	names []string
}

// NewRegistry validates tools and rejects duplicate model-visible names.
func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, executable := range tools {
		if isNilTool(executable) {
			return nil, fmt.Errorf("create tool registry: tool is nil")
		}
		definition := executable.Definition()
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("create tool registry: %w", err)
		}
		switch executable.ReplayPolicy() {
		case ReplayNever, ReplaySafe, ReplayIdempotent:
		default:
			return nil, fmt.Errorf("create tool registry %q: unsupported replay policy %q", definition.Name, executable.ReplayPolicy())
		}
		if _, exists := registry.tools[definition.Name]; exists {
			return nil, fmt.Errorf("create tool registry: duplicate tool %q", definition.Name)
		}
		registry.tools[definition.Name] = executable
		registry.names = append(registry.names, definition.Name)
	}
	sort.Strings(registry.names)
	return registry, nil
}

// Definitions returns defensive model-facing declarations ordered by name.
func (r *Registry) Definitions() []llm.ToolDefinition {
	if r == nil {
		return nil
	}
	definitions := make([]llm.ToolDefinition, 0, len(r.names))
	for _, name := range r.names {
		definition := r.tools[name].Definition()
		definition.InputSchema = append([]byte(nil), definition.InputSchema...)
		definitions = append(definitions, definition)
	}
	return definitions
}

// Lookup returns a registered executable tool by its exact model-visible name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	executable, exists := r.tools[name]
	return executable, exists
}

// Execute validates and dispatches a call without emitting activities or writing storage.
func (r *Registry) Execute(ctx context.Context, call Call, progress ProgressSink) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	modelCall := llm.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
	if err := modelCall.Validate(); err != nil {
		return Result{}, err
	}
	executable, exists := r.Lookup(call.Name)
	if !exists {
		return Result{}, fmt.Errorf("execute tool %q: tool is not registered", call.Name)
	}
	result, err := executable.Execute(ctx, Call{
		ID:             call.ID,
		Name:           call.Name,
		Arguments:      append([]byte(nil), call.Arguments...),
		IdempotencyKey: call.IdempotencyKey,
	}, progress)
	if err != nil {
		return Result{}, err
	}
	if err := result.Validate(); err != nil {
		return Result{}, fmt.Errorf("execute tool %q: %w", call.Name, err)
	}
	return result.Clone(), nil
}

// Resume dispatches a durable interrupted call to a resumable tool. Tools that do
// not implement ResumableTool use the externally supplied resolution unchanged.
func (r *Registry) Resume(ctx context.Context, call Call, interrupt Interrupt, resolution Result, progress ProgressSink) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := resolution.Validate(); err != nil {
		return Result{}, fmt.Errorf("resume tool %q: %w", call.Name, err)
	}
	executable, exists := r.Lookup(call.Name)
	if !exists {
		return Result{}, fmt.Errorf("resume tool %q: tool is not registered", call.Name)
	}
	resumable, supportsResume := executable.(ResumableTool)
	if !supportsResume {
		return resolution.Clone(), nil
	}
	result, err := resumable.Resume(ctx, Call{
		ID:             call.ID,
		Name:           call.Name,
		Arguments:      append([]byte(nil), call.Arguments...),
		IdempotencyKey: call.IdempotencyKey,
	}, Interrupt{ID: interrupt.ID, Kind: interrupt.Kind, Payload: append([]byte(nil), interrupt.Payload...)}, resolution.Clone(), progress)
	if err != nil {
		return Result{}, err
	}
	if err := result.Validate(); err != nil {
		return Result{}, fmt.Errorf("resume tool %q: %w", call.Name, err)
	}
	if result.Status == ResultInterrupted {
		return Result{}, fmt.Errorf("resume tool %q: resumed execution cannot interrupt again", call.Name)
	}
	return result.Clone(), nil
}

func isNilTool(value Tool) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
