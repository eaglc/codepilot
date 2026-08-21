package tool

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

type stubTool struct {
	definition Definition
}

func TestRegistrySupportsConcurrentReadsAfterRegistration(t *testing.T) {
	registry := NewRegistry()
	registered := &stubTool{definition: Definition{
		Name:        "read_file",
		Description: "Read one file.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}
	if err := registry.Register(registered); err != nil {
		t.Fatalf("register tool: %v", err)
	}
	var waitGroup sync.WaitGroup
	for range 32 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for range 100 {
				value, exists := registry.Lookup("read_file")
				if !exists || value != registered || len(registry.List()) != 1 || len(registry.Definitions()) != 1 {
					t.Errorf("concurrent registry read returned inconsistent data")
					return
				}
			}
		}()
	}
	waitGroup.Wait()
}

func (s *stubTool) Definition() Definition {
	return s.definition
}

func (s *stubTool) Invoke(context.Context, json.RawMessage) (Result, error) {
	return Result{Status: ResultCompleted}, nil
}

func TestRegistry_RegisterPreservesOrderAndCopiesDefinitions(t *testing.T) {
	registry := NewRegistry()
	firstSchema := json.RawMessage(`{"type":"object"}`)

	first := &stubTool{definition: Definition{
		Name:        "read_file",
		Description: "Read a bounded file range.",
		InputSchema: firstSchema,
	}}
	second := &stubTool{definition: Definition{
		Name:        "git_status",
		Description: "Read Git worktree status.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}

	if err := registry.Register(first); err != nil {
		t.Fatalf("register first tool: %v", err)
	}
	if err := registry.Register(second); err != nil {
		t.Fatalf("register second tool: %v", err)
	}

	firstSchema[0] = '['
	definitions := registry.Definitions()
	if definitions[0].Name != "read_file" || definitions[1].Name != "git_status" {
		t.Fatalf("unexpected definition order: %#v", definitions)
	}
	if string(definitions[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("registry retained mutable schema: %s", definitions[0].InputSchema)
	}

	definitions[0].InputSchema[0] = '['
	if got := string(registry.Definitions()[0].InputSchema); got != `{"type":"object"}` {
		t.Fatalf("definitions exposed internal schema: %s", got)
	}

	tools := registry.List()
	tools[0] = second
	registered, ok := registry.Lookup("read_file")
	if !ok || registered != first {
		t.Fatal("list mutation changed registry entries")
	}
}

func TestRegistry_RegisterRejectsInvalidTools(t *testing.T) {
	validSchema := json.RawMessage(`{"type":"object"}`)
	var typedNil *stubTool

	tests := []struct {
		name    string
		tool    Tool
		wantErr error
	}{
		{
			name:    "nil tool",
			wantErr: ErrNilTool,
		},
		{
			name:    "typed nil tool",
			tool:    typedNil,
			wantErr: ErrNilTool,
		},
		{
			name: "empty name",
			tool: &stubTool{definition: Definition{
				Description: "description",
				InputSchema: validSchema,
			}},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "invalid name",
			tool: &stubTool{definition: Definition{
				Name:        "ReadFile",
				Description: "description",
				InputSchema: validSchema,
			}},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty description",
			tool: &stubTool{definition: Definition{
				Name:        "read_file",
				InputSchema: validSchema,
			}},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "array schema",
			tool: &stubTool{definition: Definition{
				Name:        "read_file",
				Description: "description",
				InputSchema: json.RawMessage(`[]`),
			}},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "invalid json schema",
			tool: &stubTool{definition: Definition{
				Name:        "read_file",
				Description: "description",
				InputSchema: json.RawMessage(`{`),
			}},
			wantErr: ErrInvalidDefinition,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			err := registry.Register(test.tool)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("got error %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestRegistry_RegisterRejectsDuplicateName(t *testing.T) {
	registry := NewRegistry()
	definition := Definition{
		Name:        "read_file",
		Description: "Read a bounded file range.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}

	if err := registry.Register(&stubTool{definition: definition}); err != nil {
		t.Fatalf("register first tool: %v", err)
	}

	err := registry.Register(&stubTool{definition: definition})
	if !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("got error %v, want %v", err, ErrDuplicateTool)
	}
}
