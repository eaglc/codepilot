package agent

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryCheckPointStoreCopiesValuesAndDeletes(t *testing.T) {
	t.Parallel()
	store := NewMemoryCheckPointStore()
	original := []byte("checkpoint")
	if err := store.Set(context.Background(), "turn-1", original); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	original[0] = 'X'

	loaded, exists, err := store.Get(context.Background(), "turn-1")
	if err != nil || !exists {
		t.Fatalf("Get() = %q, %v, %v", loaded, exists, err)
	}
	if string(loaded) != "checkpoint" {
		t.Fatalf("Get() value = %q", loaded)
	}
	loaded[0] = 'Y'
	reloaded, _, err := store.Get(context.Background(), "turn-1")
	if err != nil || string(reloaded) != "checkpoint" {
		t.Fatalf("second Get() = %q, %v", reloaded, err)
	}

	if err := store.Delete(context.Background(), "turn-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, exists, err := store.Get(context.Background(), "turn-1"); err != nil || exists {
		t.Fatalf("Get() after delete exists = %v, error = %v", exists, err)
	}
}

func TestMemoryCheckPointStoreHonorsContextAndClose(t *testing.T) {
	t.Parallel()
	store := NewMemoryCheckPointStore()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Set(cancelled, "turn-1", []byte("value")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Set() error = %v, want context.Canceled", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, _, err := store.Get(context.Background(), "turn-1"); !errors.Is(err, ErrCheckPointStoreClosed) {
		t.Fatalf("Get() error = %v, want ErrCheckPointStoreClosed", err)
	}
}
