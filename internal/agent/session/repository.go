package session

import (
	"context"
	"errors"
)

// ErrNotFound identifies an Agent session lookup that found no durable metadata.
var ErrNotFound = errors.New("agent session not found")

// Repository persists Agent sessions and assigns one shared sequence to entries and records.
type Repository interface {
	Create(ctx context.Context, metadata Metadata) error
	Load(ctx context.Context, id ID) (Snapshot, error)
	List(ctx context.Context) ([]Metadata, error)
	SetArchived(ctx context.Context, id ID, archived bool) error
	AppendEntry(ctx context.Context, id ID, lane Lane, entry Entry) (Entry, error)
	AppendRecord(ctx context.Context, id ID, lane Lane, record Record) (Record, error)
	ForkLane(ctx context.Context, id ID, lane Lane, from EntryID) error
}
