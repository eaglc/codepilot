package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// ErrCheckPointStoreClosed indicates access after checkpoint teardown.
var ErrCheckPointStoreClosed = errors.New("checkpoint store is closed")

// CheckPointStore is the minimal persistence contract consumed by Eino Runner.
type CheckPointStore interface {
	Set(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, bool, error)
}

// MemoryCheckPointStore retains resumable state only for the current process.
type MemoryCheckPointStore struct {
	mu     sync.RWMutex
	values map[string][]byte
	closed bool
}

// NewMemoryCheckPointStore creates an empty process-local checkpoint store.
func NewMemoryCheckPointStore() *MemoryCheckPointStore {
	return &MemoryCheckPointStore{values: make(map[string][]byte)}
}

// Set stores a defensive copy of one checkpoint.
func (s *MemoryCheckPointStore) Set(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("set checkpoint: key is required")
	}
	if s == nil {
		return errors.New("set checkpoint: store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrCheckPointStoreClosed
	}
	s.values[key] = append([]byte(nil), value...)
	return nil
}

// Get returns a defensive copy of one checkpoint when present.
func (s *MemoryCheckPointStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, false, errors.New("get checkpoint: key is required")
	}
	if s == nil {
		return nil, false, errors.New("get checkpoint: store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, false, ErrCheckPointStoreClosed
	}
	value, exists := s.values[key]
	if !exists {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

// Delete removes one checkpoint. Its signature also satisfies Eino's optional
// CheckPointDeleter contract for automatic lifecycle cleanup.
func (s *MemoryCheckPointStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("delete checkpoint: key is required")
	}
	if s == nil {
		return errors.New("delete checkpoint: store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrCheckPointStoreClosed
	}
	delete(s.values, key)
	return nil
}

// Close clears all transient checkpoints and rejects future access.
func (s *MemoryCheckPointStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	clear(s.values)
	s.closed = true
	return nil
}
