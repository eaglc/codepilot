package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

func TestConfig_MissingFileUsesDefaultsWithoutCreatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load missing config: %v", err)
	}
	if !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("got %#v, want defaults %#v", got, Defaults())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing load created a file: %v", err)
	}
}

func TestConfig_SaveLoadStrictRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	value := Defaults()
	value.Defaults = DefaultConfig{
		ProviderProfileID: "prv_test",
		ModelID:           "test-model",
	}

	if err := Save(path, value); err != nil {
		t.Fatalf("save config: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !reflect.DeepEqual(loaded, value) {
		t.Fatalf("loaded %#v, want %#v", loaded, value)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(content), "max_turn_duration: 20m0s") {
		t.Fatalf("duration is not human-readable:\n%s", content)
	}

	value.Agent.MaxSteps = 31
	if err := Save(path, value); err != nil {
		t.Fatalf("replace config: %v", err)
	}
	loaded, err = Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.Agent.MaxSteps != 31 {
		t.Fatalf("max steps = %d, want 31", loaded.Agent.MaxSteps)
	}
}

func TestConfig_LoadRejectsUnknownFieldAndNewerVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "unknown field",
			content: `version: 1
defaults: {}
agent:
  max_steps: 30
  max_turn_duration: 20m
  command_timeout: 5m
  tool_result_max_bytes: 65536
  command_output_max_bytes: 262144
  max_stepz: 10
`,
		},
		{
			name: "newer version",
			content: `version: 2
defaults: {}
agent:
  max_steps: 30
  max_turn_duration: 20m
  command_timeout: 5m
  tool_result_max_bytes: 65536
  command_output_max_bytes: 262144
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("load succeeded, want strict validation error")
			}
		})
	}
}

func TestConfig_ValidateRejectsUnboundedValues(t *testing.T) {
	value := Defaults()
	value.Agent.MaxSteps = 0
	if err := Validate(value); err == nil {
		t.Fatal("zero max steps was accepted")
	}

	value = Defaults()
	value.Agent.CommandTimeout = value.Agent.MaxTurnDuration + time.Second
	if err := Validate(value); err == nil {
		t.Fatal("command timeout longer than turn was accepted")
	}

	value = Defaults()
	value.Defaults.ProviderProfileID = session.ProviderProfileID("prv_test")
	if err := Validate(value); err == nil {
		t.Fatal("provider default without model was accepted")
	}
}

func TestValidatePaths(t *testing.T) {
	absolute := t.TempDir()
	if err := ValidatePaths(Paths{ConfigDir: absolute, StateDir: filepath.Join(absolute, "State")}); err != nil {
		t.Fatalf("validate absolute paths: %v", err)
	}
	if err := ValidatePaths(Paths{ConfigDir: "relative", StateDir: absolute}); err == nil {
		t.Fatal("relative config path was accepted")
	}
}
