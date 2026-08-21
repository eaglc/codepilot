package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

var (
	// ErrNilTool indicates that a nil Tool was registered.
	ErrNilTool = errors.New("tool is nil")
	// ErrInvalidDefinition indicates that a tool definition violates the registry contract.
	ErrInvalidDefinition = errors.New("tool definition is invalid")
	// ErrDuplicateTool indicates that a tool name is already registered.
	ErrDuplicateTool = errors.New("tool name is already registered")
)

type registryEntry struct {
	tool       Tool
	definition Definition
}

// Registry stores the immutable tool set available to one agent turn.
type Registry struct {
	entries map[string]registryEntry
	names   []string
}

// NewRegistry creates an empty per-turn tool registry.
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[string]registryEntry),
	}
}

// Register validates and adds a tool while preserving registration order.
func (r *Registry) Register(value Tool) error {
	if r == nil {
		return fmt.Errorf("register tool: %w", ErrInvalidDefinition)
	}

	if isNilTool(value) {
		return fmt.Errorf("register tool: %w", ErrNilTool)
	}

	definition := value.Definition()
	if err := validateDefinition(definition); err != nil {
		return fmt.Errorf("register tool %q: %w", definition.Name, err)
	}

	if _, exists := r.entries[definition.Name]; exists {
		return fmt.Errorf("register tool %q: %w", definition.Name, ErrDuplicateTool)
	}

	definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	r.entries[definition.Name] = registryEntry{
		tool:       value,
		definition: definition,
	}
	r.names = append(r.names, definition.Name)

	return nil
}

// Lookup returns the tool registered under name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}

	entry, exists := r.entries[name]
	return entry.tool, exists
}

// List returns tools in registration order using a new slice.
func (r *Registry) List() []Tool {
	if r == nil {
		return nil
	}

	values := make([]Tool, 0, len(r.names))
	for _, name := range r.names {
		values = append(values, r.entries[name].tool)
	}

	return values
}

// Definitions returns defensive copies in registration order.
func (r *Registry) Definitions() []Definition {
	if r == nil {
		return nil
	}

	definitions := make([]Definition, 0, len(r.names))
	for _, name := range r.names {
		definition := r.entries[name].definition
		definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
		definitions = append(definitions, definition)
	}

	return definitions
}

func validateDefinition(definition Definition) error {
	if !isSnakeCaseName(definition.Name) {
		return fmt.Errorf("%w: name must use lower snake_case", ErrInvalidDefinition)
	}

	if strings.TrimSpace(definition.Description) == "" {
		return fmt.Errorf("%w: description is empty", ErrInvalidDefinition)
	}

	var schema map[string]json.RawMessage
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil || schema == nil {
		return fmt.Errorf("%w: input schema must be a JSON object", ErrInvalidDefinition)
	}

	return nil
}

func isSnakeCaseName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' || name[len(name)-1] == '_' {
		return false
	}

	previousUnderscore := false
	for _, character := range name {
		isLowercase := character >= 'a' && character <= 'z'
		isDigit := character >= '0' && character <= '9'
		isUnderscore := character == '_'
		if !isLowercase && !isDigit && !isUnderscore {
			return false
		}
		if isUnderscore && previousUnderscore {
			return false
		}
		previousUnderscore = isUnderscore
	}

	return true
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
