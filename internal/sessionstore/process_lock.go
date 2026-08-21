package sessionstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gofrs/flock"
)

// ErrStateInUse indicates that another CodePilot process owns StateDir.
var ErrStateInUse = errors.New("CodePilot state directory is already in use")

// ProcessLock owns the exclusive StateDir lock for one application process.
type ProcessLock struct {
	mu     sync.Mutex
	file   *flock.Flock
	closed bool
}

// AcquireProcessLock obtains the process-wide StateDir lock without blocking.
func AcquireProcessLock(stateDir string) (*ProcessLock, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("acquire state lock: state directory is empty")
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("acquire state lock: resolve state directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("acquire state lock: create state directory: %w", err)
	}

	file := flock.New(filepath.Join(absolute, ".lock"))
	locked, err := file.TryLock()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire state lock: %w", err)
	}
	if !locked {
		_ = file.Close()
		return nil, ErrStateInUse
	}

	return &ProcessLock{file: file}, nil
}

// Close releases the lock. The .lock file remains as an ownership marker.
func (l *ProcessLock) Close() error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}
	l.closed = true

	if err := l.file.Close(); err != nil {
		return fmt.Errorf("release state lock: %w", err)
	}

	return nil
}
