package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/session"
	"go.yaml.in/yaml/v3"
)

const (
	currentConfigVersion = 1
	maxStepsUpperBound   = 1000
	maxDuration          = 24 * time.Hour
	maxOutputBytes       = 64 << 20
)

// Config is the versioned, non-sensitive process configuration.
type Config struct {
	Version  int           `yaml:"version"`
	Defaults DefaultConfig `yaml:"defaults"`
	Agent    AgentConfig   `yaml:"agent"`
}

// DefaultConfig contains defaults copied into newly configured sessions.
type DefaultConfig struct {
	ProviderProfileID session.ProviderProfileID `yaml:"provider_profile_id,omitempty"`
	ModelID           string                    `yaml:"model_id,omitempty"`
}

// AgentConfig contains hard upper bounds used for each agent turn.
type AgentConfig struct {
	MaxSteps              int           `yaml:"max_steps"`
	MaxTurnDuration       time.Duration `yaml:"max_turn_duration"`
	CommandTimeout        time.Duration `yaml:"command_timeout"`
	ToolResultMaxBytes    int           `yaml:"tool_result_max_bytes"`
	CommandOutputMaxBytes int           `yaml:"command_output_max_bytes"`
}

// RunLimits converts persisted settings to the session runtime contract.
func (c AgentConfig) RunLimits() session.RunLimits {
	return session.RunLimits{
		MaxSteps:              c.MaxSteps,
		MaxTurnDuration:       c.MaxTurnDuration,
		CommandTimeout:        c.CommandTimeout,
		ToolResultMaxBytes:    c.ToolResultMaxBytes,
		CommandOutputMaxBytes: c.CommandOutputMaxBytes,
	}
}

// Defaults returns the safe built-in configuration for a fresh installation.
func Defaults() Config {
	return Config{
		Version: currentConfigVersion,
		Agent: AgentConfig{
			MaxSteps:              30,
			MaxTurnDuration:       20 * time.Minute,
			CommandTimeout:        5 * time.Minute,
			ToolResultMaxBytes:    64 << 10,
			CommandOutputMaxBytes: 256 << 10,
		},
	}
}

// Load reads a strict version-one config. A missing file uses defaults without
// creating anything on disk.
func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("load config: path is empty")
	}

	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	var value Config
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := ensureYAMLEOF(decoder); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := Validate(value); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}

	return value, nil
}

// Save atomically replaces a strict version-one config file.
func Save(path string, value Config) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("save config: path is empty")
	}
	if err := Validate(value); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := writeYAMLAtomic(path, value); err != nil {
		return fmt.Errorf("save config %q: %w", path, err)
	}

	return nil
}

// Validate enforces bounded values; zero never means unlimited.
func Validate(value Config) error {
	if value.Version != currentConfigVersion {
		return fmt.Errorf("unsupported version %d", value.Version)
	}
	if (value.Defaults.ProviderProfileID == "") != (strings.TrimSpace(value.Defaults.ModelID) == "") {
		return errors.New("default provider profile and model must be set together")
	}
	if value.Agent.MaxSteps < 1 || value.Agent.MaxSteps > maxStepsUpperBound {
		return fmt.Errorf("agent max steps must be between 1 and %d", maxStepsUpperBound)
	}
	if err := validateDuration("max turn duration", value.Agent.MaxTurnDuration); err != nil {
		return err
	}
	if err := validateDuration("command timeout", value.Agent.CommandTimeout); err != nil {
		return err
	}
	if value.Agent.CommandTimeout > value.Agent.MaxTurnDuration {
		return errors.New("command timeout must not exceed max turn duration")
	}
	if err := validateOutputLimit("tool result max bytes", value.Agent.ToolResultMaxBytes); err != nil {
		return err
	}
	if err := validateOutputLimit("command output max bytes", value.Agent.CommandOutputMaxBytes); err != nil {
		return err
	}

	return nil
}

func validateDuration(name string, value time.Duration) error {
	if value < time.Second || value > maxDuration {
		return fmt.Errorf("agent %s must be between 1s and %s", name, maxDuration)
	}

	return nil
}

func validateOutputLimit(name string, value int) error {
	if value < 1 || value > maxOutputBytes {
		return fmt.Errorf("agent %s must be between 1 and %d", name, maxOutputBytes)
	}

	return nil
}

func ensureYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}

	return errors.New("multiple YAML documents are not allowed")
}

func writeYAMLAtomic(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".codepilot-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary file permissions: %w", err)
	}
	encoder := yaml.NewEncoder(temporary)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode temporary file: %w", err)
	}
	if err := encoder.Close(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("close YAML encoder: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace destination file: %w", err)
	}
	removeTemporary = false

	return nil
}
