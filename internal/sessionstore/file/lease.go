package file

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gofrs/flock"
)

const stateLeaseVersion = 1

// ErrStateInUse is matched when another process currently owns the StateDir.
var ErrStateInUse = errors.New("CodePilot state directory is already in use")

// LeaseOwner contains bounded, non-secret diagnostics for the current or most
// recent StateDir owner. It is never used to decide whether a lock is stale.
type LeaseOwner struct {
	Version    int        `json:"version"`
	OwnerID    string     `json:"owner_id"`
	PID        int        `json:"pid"`
	Host       string     `json:"host"`
	AcquiredAt time.Time  `json:"acquired_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}

// LeaseInUseError reports safe diagnostics while supporting errors.Is.
type LeaseInUseError struct{ Owner LeaseOwner }

func (e *LeaseInUseError) Error() string {
	if e == nil || e.Owner.PID <= 0 {
		return ErrStateInUse.Error()
	}
	detail := fmt.Sprintf("%s (pid %d", ErrStateInUse, e.Owner.PID)
	if e.Owner.Host != "" {
		detail += " on " + e.Owner.Host
	}
	if !e.Owner.AcquiredAt.IsZero() {
		detail += ", acquired " + e.Owner.AcquiredAt.UTC().Format(time.RFC3339)
	}
	return detail + ")"
}

func (*LeaseInUseError) Is(target error) bool { return target == ErrStateInUse }

// StateLease holds the process-wide StateDir writer lock. The operating system
// releases it when the process exits, including abnormal termination.
type StateLease struct {
	mu     sync.Mutex
	lock   *flock.Flock
	owner  LeaseOwner
	meta   string
	closed bool
}

// StateInspectionLease briefly excludes writers while a read-only consistency
// snapshot is assembled. It does not update owner diagnostics.
type StateInspectionLease struct {
	mu     sync.Mutex
	lock   *flock.Flock
	closed bool
}

// AcquireStateInspectionLease obtains the same OS lock as a writer without
// changing writer owner metadata or deleting either lease file.
func AcquireStateInspectionLease(stateDir string) (*StateInspectionLease, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("acquire StateDir inspection lease: path is required")
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("acquire StateDir inspection lease: resolve path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if _, err := os.Stat(absolute); err != nil {
		return nil, fmt.Errorf("acquire StateDir inspection lease: %w", err)
	}
	processLock := flock.New(filepath.Join(absolute, ".codepilot-writer.lock"))
	locked, err := processLock.TryLock()
	if err != nil {
		_ = processLock.Close()
		return nil, fmt.Errorf("acquire StateDir inspection lease: %w", err)
	}
	if !locked {
		_ = processLock.Close()
		return nil, &LeaseInUseError{Owner: readLeaseOwner(filepath.Join(absolute, ".codepilot-writer.json"))}
	}
	return &StateInspectionLease{lock: processLock}, nil
}

// Close releases a read-only inspection boundary.
func (l *StateInspectionLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if err := l.lock.Close(); err != nil {
		return fmt.Errorf("release StateDir inspection lease: %w", err)
	}
	return nil
}

// AcquireStateLease obtains a non-blocking exclusive writer lease. Existing
// lock files are never deleted or treated as proof of ownership.
func AcquireStateLease(stateDir string) (*StateLease, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("acquire StateDir lease: path is required")
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("acquire StateDir lease: resolve path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("acquire StateDir lease: create directory: %w", err)
	}
	lockPath := filepath.Join(absolute, ".codepilot-writer.lock")
	metadataPath := filepath.Join(absolute, ".codepilot-writer.json")
	processLock := flock.New(lockPath)
	locked, err := processLock.TryLock()
	if err != nil {
		_ = processLock.Close()
		return nil, fmt.Errorf("acquire StateDir lease: %w", err)
	}
	if !locked {
		_ = processLock.Close()
		owner := readLeaseOwner(metadataPath)
		return nil, &LeaseInUseError{Owner: owner}
	}
	ownerID, err := newLeaseOwnerID()
	if err != nil {
		_ = processLock.Close()
		return nil, fmt.Errorf("acquire StateDir lease: %w", err)
	}
	host, _ := os.Hostname()
	owner := LeaseOwner{
		Version: stateLeaseVersion, OwnerID: ownerID, PID: os.Getpid(),
		Host: safeLeaseLabel(host), AcquiredAt: time.Now().UTC(),
	}
	if err := writeJSONAtomic(metadataPath, owner); err != nil {
		_ = processLock.Close()
		return nil, fmt.Errorf("acquire StateDir lease: write owner diagnostics: %w", err)
	}
	return &StateLease{lock: processLock, owner: owner, meta: metadataPath}, nil
}

// Owner returns a defensive copy of this process's lease diagnostics.
func (l *StateLease) Owner() LeaseOwner {
	if l == nil {
		return LeaseOwner{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owner
}

// Close records a clean release and relinquishes the operating-system lock.
// Neither the lock file nor unknown owner metadata is deleted.
func (l *StateLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	now := time.Now().UTC()
	l.owner.ReleasedAt = &now
	metadataErr := writeJSONAtomic(l.meta, l.owner)
	lockErr := l.lock.Close()
	if metadataErr != nil {
		metadataErr = fmt.Errorf("release StateDir lease: write owner diagnostics: %w", metadataErr)
	}
	if lockErr != nil {
		lockErr = fmt.Errorf("release StateDir lease: %w", lockErr)
	}
	return errors.Join(metadataErr, lockErr)
}

func readLeaseOwner(path string) LeaseOwner {
	var owner LeaseOwner
	found, err := readJSON(path, &owner)
	if err != nil || !found || owner.Version != stateLeaseVersion || owner.PID <= 0 || strings.TrimSpace(owner.OwnerID) == "" {
		return LeaseOwner{}
	}
	owner.Host = safeLeaseLabel(owner.Host)
	return owner
}

func newLeaseOwnerID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "owner_" + hex.EncodeToString(value), nil
}

func safeLeaseLabel(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsDigit(current) || current == '.' || current == '-' || current == '_' {
			builder.WriteRune(current)
		}
		if builder.Len() >= 128 {
			break
		}
	}
	return builder.String()
}
