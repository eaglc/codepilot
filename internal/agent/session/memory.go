package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryRepository is a deterministic in-process Repository implementation for tests and ephemeral sessions.
type MemoryRepository struct {
	mu       sync.RWMutex
	sessions map[ID]*memorySession
}

type memorySession struct {
	metadata  Metadata
	entries   []Entry
	records   []Record
	log       []LogItem
	lanes     map[Lane]EntryID
	entryIDs  map[EntryID]struct{}
	recordIDs map[RecordID]struct{}
	sequence  uint64
}

// NewMemoryRepository creates an empty repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{sessions: make(map[ID]*memorySession)}
}

// Create stores new session metadata and initializes its main lane.
func (r *MemoryRepository) Create(ctx context.Context, metadata Metadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if metadata.ID == "" {
		return errors.New("create agent session: id is required")
	}
	now := time.Now().UTC()
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = now
	}
	if metadata.UpdatedAt.IsZero() {
		metadata.UpdatedAt = metadata.CreatedAt
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[metadata.ID]; exists {
		return fmt.Errorf("create agent session %q: already exists", metadata.ID)
	}
	r.sessions[metadata.ID] = &memorySession{
		metadata:  metadata,
		lanes:     map[Lane]EntryID{MainLane: ""},
		entryIDs:  make(map[EntryID]struct{}),
		recordIDs: make(map[RecordID]struct{}),
	}
	return nil
}

// Load returns an isolated snapshot ordered by the shared append sequence.
func (r *MemoryRepository) Load(ctx context.Context, id ID) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	stored, exists := r.sessions[id]
	if !exists {
		return Snapshot{}, fmt.Errorf("load agent session %q: %w", id, ErrNotFound)
	}
	return cloneSnapshot(stored), nil
}

// List returns stable Agent session metadata ordering by ID.
func (r *MemoryRepository) List(ctx context.Context) ([]Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]Metadata, 0, len(r.sessions))
	for _, stored := range r.sessions {
		values = append(values, stored.metadata)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

// SetArchived updates lifecycle metadata without deleting the conversation journal.
func (r *MemoryRepository) SetArchived(ctx context.Context, id ID, archived bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, exists := r.sessions[id]
	if !exists {
		return fmt.Errorf("archive agent session %q: %w", id, ErrNotFound)
	}
	if stored.metadata.Archived == archived {
		return nil
	}
	stored.metadata.Archived = archived
	stored.metadata.UpdatedAt = time.Now().UTC()
	return nil
}

// AppendEntry assigns parent, lane, timestamp and shared sequence atomically.
func (r *MemoryRepository) AppendEntry(ctx context.Context, id ID, lane Lane, entry Entry) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if err := entry.Validate(); err != nil {
		return Entry{}, err
	}
	if lane == "" {
		return Entry{}, errors.New("append agent session entry: lane is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, exists := r.sessions[id]
	if !exists {
		return Entry{}, fmt.Errorf("append agent session entry: session %q not found", id)
	}
	leaf, exists := stored.lanes[lane]
	if !exists {
		return Entry{}, fmt.Errorf("append agent session entry: lane %q not found", lane)
	}
	if _, duplicate := stored.entryIDs[entry.ID]; duplicate {
		return Entry{}, fmt.Errorf("append agent session entry: duplicate id %q", entry.ID)
	}
	stored.sequence++
	entry.Sequence = stored.sequence
	entry.Lane = lane
	entry.ParentID = leaf
	entry.Timestamp = time.Now().UTC()
	entry = cloneEntry(entry)
	stored.entries = append(stored.entries, entry)
	stored.log = append(stored.log, LogItem{Sequence: entry.Sequence, Entry: entryPointer(entry)})
	stored.entryIDs[entry.ID] = struct{}{}
	stored.lanes[lane] = entry.ID
	stored.metadata.UpdatedAt = entry.Timestamp
	return cloneEntry(entry), nil
}

// AppendRecord assigns timestamp and shared sequence atomically without changing a lane leaf.
func (r *MemoryRepository) AppendRecord(ctx context.Context, id ID, lane Lane, record Record) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	if lane == "" {
		return Record{}, errors.New("append agent session record: lane is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, exists := r.sessions[id]
	if !exists {
		return Record{}, fmt.Errorf("append agent session record: session %q not found", id)
	}
	if _, exists := stored.lanes[lane]; !exists {
		return Record{}, fmt.Errorf("append agent session record: lane %q not found", lane)
	}
	if _, duplicate := stored.recordIDs[record.ID]; duplicate {
		return Record{}, fmt.Errorf("append agent session record: duplicate id %q", record.ID)
	}
	stored.sequence++
	record.Sequence = stored.sequence
	record.Lane = lane
	record.Timestamp = time.Now().UTC()
	record = cloneRecord(record)
	stored.records = append(stored.records, record)
	stored.log = append(stored.log, LogItem{Sequence: record.Sequence, Record: recordPointer(record)})
	stored.recordIDs[record.ID] = struct{}{}
	stored.metadata.UpdatedAt = record.Timestamp
	return cloneRecord(record), nil
}

// ForkLane creates a new lane at an existing entry or at the empty root.
func (r *MemoryRepository) ForkLane(ctx context.Context, id ID, lane Lane, from EntryID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lane == "" {
		return errors.New("fork agent session lane: lane is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, exists := r.sessions[id]
	if !exists {
		return fmt.Errorf("fork agent session lane: session %q not found", id)
	}
	if _, exists := stored.lanes[lane]; exists {
		return fmt.Errorf("fork agent session lane: lane %q already exists", lane)
	}
	if from != "" {
		if _, exists := stored.entryIDs[from]; !exists {
			return fmt.Errorf("fork agent session lane: entry %q not found", from)
		}
	}
	stored.lanes[lane] = from
	stored.sequence++
	now := time.Now().UTC()
	record := Record{
		ID: RecordID(fmt.Sprintf("lane-fork:%s:%d", lane, stored.sequence)), Sequence: stored.sequence,
		Lane: MainLane, Timestamp: now, Type: RecordLaneForked, LaneFork: &LaneForkData{Lane: lane, FromEntryID: from},
	}
	stored.records = append(stored.records, record)
	stored.log = append(stored.log, LogItem{Sequence: record.Sequence, Record: recordPointer(record)})
	stored.recordIDs[record.ID] = struct{}{}
	stored.metadata.UpdatedAt = now
	return nil
}

func cloneSnapshot(stored *memorySession) Snapshot {
	snapshot := Snapshot{Metadata: stored.metadata}
	snapshot.Entries = make([]Entry, len(stored.entries))
	for index, entry := range stored.entries {
		snapshot.Entries[index] = cloneEntry(entry)
	}
	snapshot.Records = make([]Record, len(stored.records))
	for index, record := range stored.records {
		snapshot.Records[index] = cloneRecord(record)
	}
	for lane, leaf := range stored.lanes {
		snapshot.Lanes = append(snapshot.Lanes, LanePointer{Lane: lane, LeafID: leaf})
	}
	sort.Slice(snapshot.Lanes, func(left, right int) bool { return snapshot.Lanes[left].Lane < snapshot.Lanes[right].Lane })
	snapshot.Log = make([]LogItem, len(stored.log))
	for index, item := range stored.log {
		snapshot.Log[index].Sequence = item.Sequence
		if item.Entry != nil {
			entry := cloneEntry(*item.Entry)
			snapshot.Log[index].Entry = &entry
		}
		if item.Record != nil {
			record := cloneRecord(*item.Record)
			snapshot.Log[index].Record = &record
		}
	}
	return snapshot
}

func cloneEntry(entry Entry) Entry {
	return entry.Clone()
}

func cloneRecord(record Record) Record {
	return record.Clone()
}

func entryPointer(entry Entry) *Entry {
	clone := cloneEntry(entry)
	return &clone
}

func recordPointer(record Record) *Record {
	clone := cloneRecord(record)
	return &clone
}
