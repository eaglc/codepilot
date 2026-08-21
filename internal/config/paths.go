package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Paths separates human-managed configuration from durable application state.
type Paths struct {
	ConfigDir string
	StateDir  string
}

// ResolvePaths returns the platform-specific CodePilot directories.
func ResolvePaths() (Paths, error) {
	configBase, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
	}

	var paths Paths
	switch runtime.GOOS {
	case "windows":
		stateBase := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if stateBase == "" {
			stateBase, err = os.UserCacheDir()
			if err != nil {
				return Paths{}, fmt.Errorf("resolve user state directory: %w", err)
			}
		}
		paths = Paths{
			ConfigDir: filepath.Join(configBase, "CodePilot"),
			StateDir:  filepath.Join(stateBase, "CodePilot", "State"),
		}
	case "darwin":
		paths = Paths{
			ConfigDir: filepath.Join(configBase, "CodePilot"),
			StateDir:  filepath.Join(configBase, "CodePilot", "State"),
		}
	default:
		stateBase := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
		if stateBase == "" {
			homeDir, homeErr := os.UserHomeDir()
			if homeErr != nil {
				return Paths{}, fmt.Errorf("resolve user state directory: %w", homeErr)
			}
			stateBase = filepath.Join(homeDir, ".local", "state")
		}
		paths = Paths{
			ConfigDir: filepath.Join(configBase, "codepilot"),
			StateDir:  filepath.Join(stateBase, "codepilot"),
		}
	}

	if err := ValidatePaths(paths); err != nil {
		return Paths{}, err
	}

	paths.ConfigDir = filepath.Clean(paths.ConfigDir)
	paths.StateDir = filepath.Clean(paths.StateDir)
	return paths, nil
}

// ValidatePaths checks explicitly supplied app paths without touching disk.
func ValidatePaths(paths Paths) error {
	if err := validateAbsolutePath("config directory", paths.ConfigDir); err != nil {
		return err
	}
	if err := validateAbsolutePath("state directory", paths.StateDir); err != nil {
		return err
	}

	return nil
}

func validateAbsolutePath(name string, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("validate paths: %s is empty", name)
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("validate paths: %s must be absolute", name)
	}

	return nil
}
