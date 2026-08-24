package session

import (
	"context"
	"time"
)

// JournalArchiveRef identifies one immutable cold copy without exposing a
// filesystem path.
type JournalArchiveRef struct {
	ID           string    `json:"id"`
	SessionID    ID        `json:"session_id"`
	CreatedAt    time.Time `json:"created_at"`
	Size         int64     `json:"size"`
	LastSequence uint64    `json:"last_sequence"`
	Codec        string    `json:"codec"`
}

// JournalArchive contains exact source files suitable for audit or offline
// reprocessing. The live journal remains authoritative for normal recovery.
type JournalArchive struct {
	Metadata []byte
	Journal  []byte
}

// JournalArchiver is an optional repository capability for immutable cold
// copies. It is intentionally separate from the live Repository contract.
type JournalArchiver interface {
	CreateJournalArchive(ctx context.Context, id ID) (JournalArchiveRef, error)
	ListJournalArchives(ctx context.Context, id ID) ([]JournalArchiveRef, error)
	LoadJournalArchive(ctx context.Context, reference JournalArchiveRef) (JournalArchive, error)
}
