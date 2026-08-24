// Package file implements durable Agent sessions with versioned JSON and JSONL files.
package file

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

const (
	metadataVersion = 1
	journalVersion  = 1
	maxJournalLine  = 32 << 20
)

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Repository persists Agent session metadata and one shared entry/record journal.
type Repository struct {
	root  string
	mu    sync.Mutex
	locks map[agentsession.ID]*sync.Mutex
}

type metadataFile struct {
	Version  int                   `json:"version"`
	Metadata agentsession.Metadata `json:"metadata"`
}

type journalLine struct {
	Version  int                  `json:"version"`
	Sequence uint64               `json:"sequence"`
	Entry    *agentsession.Entry  `json:"entry,omitempty"`
	Record   *agentsession.Record `json:"record,omitempty"`
}

// NewRepository opens or creates a file-backed Agent session repository.
func NewRepository(root string) (*Repository, error) {
	absolute, err := repositoryRoot(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(absolute, "agent-sessions"), 0o700); err != nil {
		return nil, fmt.Errorf("create file session repository: create root: %w", err)
	}
	return &Repository{root: absolute, locks: make(map[agentsession.ID]*sync.Mutex)}, nil
}

// OpenRepository opens existing Agent state without creating directories.
// It is intended for read-only consistency diagnostics.
func OpenRepository(root string) (*Repository, error) {
	absolute, err := repositoryRoot(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("open file session repository: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("open file session repository: root is not a directory")
	}
	return &Repository{root: absolute, locks: make(map[agentsession.ID]*sync.Mutex)}, nil
}

func repositoryRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("create file session repository: root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("create file session repository: resolve root: %w", err)
	}
	return filepath.Clean(absolute), nil
}

// Create persists versioned metadata for a new Agent session.
func (r *Repository) Create(ctx context.Context, metadata agentsession.Metadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(metadata.ID); err != nil {
		return err
	}
	lock := r.sessionLock(metadata.ID)
	lock.Lock()
	defer lock.Unlock()
	directory := r.sessionDirectory(metadata.ID)
	if _, err := os.Stat(directory); err == nil {
		var existing metadataFile
		found, readErr := readJSON(r.metadataPath(metadata.ID), &existing)
		if readErr != nil {
			return fmt.Errorf("create file session %q: inspect existing metadata: %w", metadata.ID, readErr)
		}
		if found {
			return fmt.Errorf("create file session %q: already exists", metadata.ID)
		}
		journal, journalErr := os.Stat(r.journalPath(metadata.ID))
		if journalErr == nil && journal.Size() != 0 {
			return fmt.Errorf("create file session %q: metadata is missing but journal is not empty", metadata.ID)
		}
		if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) {
			return fmt.Errorf("create file session %q: inspect partial journal: %w", metadata.ID, journalErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("create file session %q: inspect path: %w", metadata.ID, err)
	} else if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create file session %q: create directory: %w", metadata.ID, err)
	}
	now := time.Now().UTC()
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = now
	}
	if metadata.UpdatedAt.IsZero() {
		metadata.UpdatedAt = metadata.CreatedAt
	}
	if err := writeJSONAtomic(r.metadataPath(metadata.ID), metadataFile{Version: metadataVersion, Metadata: metadata}); err != nil {
		return fmt.Errorf("create file session %q: %w", metadata.ID, err)
	}
	return nil
}

// Load validates and reconstructs a session snapshot, ignoring only an incomplete final journal line.
func (r *Repository) Load(ctx context.Context, id agentsession.ID) (agentsession.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return agentsession.Snapshot{}, err
	}
	if err := validateSessionID(id); err != nil {
		return agentsession.Snapshot{}, err
	}
	lock := r.sessionLock(id)
	lock.Lock()
	defer lock.Unlock()
	return r.loadLocked(id)
}

// List returns stable Agent session metadata ordering without loading journals.
func (r *Repository) List(ctx context.Context) ([]agentsession.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(r.root, "agent-sessions"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list file sessions: %w", err)
	}
	values := make([]agentsession.Metadata, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		id := agentsession.ID(entry.Name())
		if err := validateSessionID(id); err != nil {
			return nil, fmt.Errorf("list file sessions: %w", err)
		}
		lock := r.sessionLock(id)
		lock.Lock()
		var stored metadataFile
		found, readErr := readJSON(r.metadataPath(id), &stored)
		lock.Unlock()
		if readErr != nil {
			return nil, fmt.Errorf("list file session %q: %w", id, readErr)
		}
		if !found {
			journal, journalErr := os.Stat(r.journalPath(id))
			if errors.Is(journalErr, os.ErrNotExist) || (journalErr == nil && journal.Size() == 0) {
				continue
			}
			if journalErr != nil {
				return nil, fmt.Errorf("list file session %q: inspect journal: %w", id, journalErr)
			}
			return nil, fmt.Errorf("list file session %q: metadata is missing but journal is not empty", id)
		}
		if stored.Version != metadataVersion || stored.Metadata.ID != id {
			return nil, fmt.Errorf("list file session %q: unsupported or mismatched metadata", id)
		}
		values = append(values, stored.Metadata)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

// SetArchived updates lifecycle metadata without deleting the session journal.
func (r *Repository) SetArchived(ctx context.Context, id agentsession.ID, archived bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(id); err != nil {
		return err
	}
	lock := r.sessionLock(id)
	lock.Lock()
	defer lock.Unlock()
	var stored metadataFile
	found, err := readJSON(r.metadataPath(id), &stored)
	if err != nil {
		return fmt.Errorf("archive file session %q: %w", id, err)
	}
	if !found {
		return fmt.Errorf("archive file session %q: %w", id, agentsession.ErrNotFound)
	}
	if stored.Version != metadataVersion || stored.Metadata.ID != id {
		return fmt.Errorf("archive file session %q: unsupported or mismatched metadata", id)
	}
	if stored.Metadata.Archived == archived {
		return nil
	}
	stored.Metadata.Archived = archived
	stored.Metadata.UpdatedAt = time.Now().UTC()
	if err := writeJSONAtomic(r.metadataPath(id), stored); err != nil {
		return fmt.Errorf("archive file session %q: %w", id, err)
	}
	return nil
}

// AppendEntry assigns parent, timestamp and shared sequence before a durable append.
func (r *Repository) AppendEntry(ctx context.Context, id agentsession.ID, lane agentsession.Lane, entry agentsession.Entry) (agentsession.Entry, error) {
	if err := ctx.Err(); err != nil {
		return agentsession.Entry{}, err
	}
	if err := entry.Validate(); err != nil {
		return agentsession.Entry{}, err
	}
	if lane == "" {
		return agentsession.Entry{}, errors.New("append file session entry: lane is required")
	}
	lock := r.sessionLock(id)
	lock.Lock()
	defer lock.Unlock()
	snapshot, err := r.loadLocked(id)
	if err != nil {
		return agentsession.Entry{}, err
	}
	leaf, found := snapshotLane(snapshot, lane)
	if !found {
		return agentsession.Entry{}, fmt.Errorf("append file session entry: lane %q not found", lane)
	}
	if entryIDExists(snapshot, entry.ID) {
		return agentsession.Entry{}, fmt.Errorf("append file session entry: duplicate id %q", entry.ID)
	}
	entry.Sequence = nextSequence(snapshot)
	entry.Lane = lane
	entry.ParentID = leaf
	entry.Timestamp = time.Now().UTC()
	stored := cloneEntry(entry)
	if err := appendJournalLine(r.journalPath(id), journalLine{Version: journalVersion, Sequence: stored.Sequence, Entry: &stored}); err != nil {
		return agentsession.Entry{}, fmt.Errorf("append file session entry: %w", err)
	}
	if err := r.touchMetadata(id, stored.Timestamp); err != nil {
		return agentsession.Entry{}, err
	}
	return cloneEntry(stored), nil
}

// AppendRecord assigns timestamp and shared sequence before a durable append.
func (r *Repository) AppendRecord(ctx context.Context, id agentsession.ID, lane agentsession.Lane, record agentsession.Record) (agentsession.Record, error) {
	if err := ctx.Err(); err != nil {
		return agentsession.Record{}, err
	}
	if record.Type == agentsession.RecordLaneForked {
		return agentsession.Record{}, errors.New("append file session record: lane forks must use ForkLane")
	}
	if err := record.Validate(); err != nil {
		return agentsession.Record{}, err
	}
	if lane == "" {
		return agentsession.Record{}, errors.New("append file session record: lane is required")
	}
	lock := r.sessionLock(id)
	lock.Lock()
	defer lock.Unlock()
	snapshot, err := r.loadLocked(id)
	if err != nil {
		return agentsession.Record{}, err
	}
	if _, found := snapshotLane(snapshot, lane); !found {
		return agentsession.Record{}, fmt.Errorf("append file session record: lane %q not found", lane)
	}
	if recordIDExists(snapshot, record.ID) {
		return agentsession.Record{}, fmt.Errorf("append file session record: duplicate id %q", record.ID)
	}
	record.Sequence = nextSequence(snapshot)
	record.Lane = lane
	record.Timestamp = time.Now().UTC()
	stored := cloneRecord(record)
	if err := appendJournalLine(r.journalPath(id), journalLine{Version: journalVersion, Sequence: stored.Sequence, Record: &stored}); err != nil {
		return agentsession.Record{}, fmt.Errorf("append file session record: %w", err)
	}
	if err := r.touchMetadata(id, stored.Timestamp); err != nil {
		return agentsession.Record{}, err
	}
	return cloneRecord(stored), nil
}

// ForkLane durably creates a new branch at an existing entry or the empty root.
func (r *Repository) ForkLane(ctx context.Context, id agentsession.ID, lane agentsession.Lane, from agentsession.EntryID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lane == "" || lane == agentsession.MainLane {
		return errors.New("fork file session lane: a non-main lane is required")
	}
	lock := r.sessionLock(id)
	lock.Lock()
	defer lock.Unlock()
	snapshot, err := r.loadLocked(id)
	if err != nil {
		return err
	}
	if _, exists := snapshotLane(snapshot, lane); exists {
		return fmt.Errorf("fork file session lane: lane %q already exists", lane)
	}
	if from != "" && !entryIDExists(snapshot, from) {
		return fmt.Errorf("fork file session lane: entry %q not found", from)
	}
	recordID, err := randomRecordID()
	if err != nil {
		return fmt.Errorf("fork file session lane: generate record id: %w", err)
	}
	now := time.Now().UTC()
	record := agentsession.Record{
		ID: recordID, Sequence: nextSequence(snapshot), Lane: agentsession.MainLane, Timestamp: now,
		Type: agentsession.RecordLaneForked, LaneFork: &agentsession.LaneForkData{Lane: lane, FromEntryID: from},
	}
	if err := appendJournalLine(r.journalPath(id), journalLine{Version: journalVersion, Sequence: record.Sequence, Record: &record}); err != nil {
		return fmt.Errorf("fork file session lane: %w", err)
	}
	return r.touchMetadata(id, now)
}

func (r *Repository) loadLocked(id agentsession.ID) (agentsession.Snapshot, error) {
	var stored metadataFile
	found, err := readJSON(r.metadataPath(id), &stored)
	if err != nil {
		return agentsession.Snapshot{}, fmt.Errorf("load file session %q metadata: %w", id, err)
	}
	if !found {
		return agentsession.Snapshot{}, fmt.Errorf("load file session %q: %w", id, agentsession.ErrNotFound)
	}
	if stored.Version != metadataVersion || stored.Metadata.ID != id {
		return agentsession.Snapshot{}, fmt.Errorf("load file session %q: unsupported or mismatched metadata", id)
	}
	lines, truncated, err := readJournal(r.journalPath(id))
	if err != nil {
		return agentsession.Snapshot{}, fmt.Errorf("load file session %q journal: %w", id, err)
	}
	snapshot, err := buildSnapshot(stored.Metadata, lines)
	if err != nil {
		return agentsession.Snapshot{}, fmt.Errorf("load file session %q journal: %w", id, err)
	}
	if truncated {
		snapshot.Warnings = append(snapshot.Warnings, agentsession.RecoveryWarning{Kind: "truncated_journal_tail", Message: "An incomplete final journal record was ignored."})
	}
	return snapshot, nil
}

func buildSnapshot(metadata agentsession.Metadata, lines []journalLine) (agentsession.Snapshot, error) {
	snapshot := agentsession.Snapshot{Metadata: metadata, Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane}}}
	lanes := map[agentsession.Lane]agentsession.EntryID{agentsession.MainLane: ""}
	entryIDs := make(map[agentsession.EntryID]struct{})
	recordIDs := make(map[agentsession.RecordID]struct{})
	var expected uint64 = 1
	for _, line := range lines {
		if line.Version != journalVersion || line.Sequence != expected || (line.Entry == nil) == (line.Record == nil) {
			return agentsession.Snapshot{}, fmt.Errorf("invalid journal envelope at sequence %d", expected)
		}
		if line.Entry != nil {
			entry := cloneEntry(*line.Entry)
			if err := validateStoredEntry(entry, line.Sequence); err != nil {
				return agentsession.Snapshot{}, err
			}
			leaf, exists := lanes[entry.Lane]
			if !exists || entry.ParentID != leaf {
				return agentsession.Snapshot{}, fmt.Errorf("entry %q has invalid lane parent", entry.ID)
			}
			if _, duplicate := entryIDs[entry.ID]; duplicate {
				return agentsession.Snapshot{}, fmt.Errorf("duplicate entry id %q", entry.ID)
			}
			entryIDs[entry.ID] = struct{}{}
			lanes[entry.Lane] = entry.ID
			snapshot.Entries = append(snapshot.Entries, entry)
			snapshot.Log = append(snapshot.Log, agentsession.LogItem{Sequence: line.Sequence, Entry: entryPointer(entry)})
		} else {
			record := cloneRecord(*line.Record)
			if err := validateStoredRecord(record, line.Sequence); err != nil {
				return agentsession.Snapshot{}, err
			}
			if _, duplicate := recordIDs[record.ID]; duplicate {
				return agentsession.Snapshot{}, fmt.Errorf("duplicate record id %q", record.ID)
			}
			if record.Type == agentsession.RecordLaneForked {
				if _, duplicate := lanes[record.LaneFork.Lane]; duplicate {
					return agentsession.Snapshot{}, fmt.Errorf("duplicate lane %q", record.LaneFork.Lane)
				}
				if record.LaneFork.FromEntryID != "" {
					if _, exists := entryIDs[record.LaneFork.FromEntryID]; !exists {
						return agentsession.Snapshot{}, fmt.Errorf("lane %q references missing entry %q", record.LaneFork.Lane, record.LaneFork.FromEntryID)
					}
				}
				lanes[record.LaneFork.Lane] = record.LaneFork.FromEntryID
			} else if _, exists := lanes[record.Lane]; !exists {
				return agentsession.Snapshot{}, fmt.Errorf("record %q references missing lane %q", record.ID, record.Lane)
			}
			recordIDs[record.ID] = struct{}{}
			snapshot.Records = append(snapshot.Records, record)
			snapshot.Log = append(snapshot.Log, agentsession.LogItem{Sequence: line.Sequence, Record: recordPointer(record)})
		}
		expected++
	}
	snapshot.Lanes = snapshot.Lanes[:0]
	for lane, leaf := range lanes {
		snapshot.Lanes = append(snapshot.Lanes, agentsession.LanePointer{Lane: lane, LeafID: leaf})
	}
	sort.Slice(snapshot.Lanes, func(left, right int) bool { return snapshot.Lanes[left].Lane < snapshot.Lanes[right].Lane })
	return snapshot, nil
}

func validateStoredEntry(entry agentsession.Entry, sequence uint64) error {
	if entry.Sequence != sequence || entry.Lane == "" || entry.Timestamp.IsZero() {
		return fmt.Errorf("entry %q has invalid storage fields", entry.ID)
	}
	provisioned := cloneEntry(entry)
	provisioned.Sequence = 0
	provisioned.Lane = ""
	provisioned.ParentID = ""
	provisioned.Timestamp = time.Time{}
	return provisioned.Validate()
}

func validateStoredRecord(record agentsession.Record, sequence uint64) error {
	if record.Sequence != sequence || record.Lane == "" || record.Timestamp.IsZero() {
		return fmt.Errorf("record %q has invalid storage fields", record.ID)
	}
	provisioned := cloneRecord(record)
	provisioned.Sequence = 0
	provisioned.Lane = ""
	provisioned.Timestamp = time.Time{}
	return provisioned.Validate()
}

func (r *Repository) touchMetadata(id agentsession.ID, updatedAt time.Time) error {
	var stored metadataFile
	found, err := readJSON(r.metadataPath(id), &stored)
	if err != nil {
		return fmt.Errorf("update file session metadata: %w", err)
	}
	if !found {
		return fmt.Errorf("update file session metadata: session %q not found", id)
	}
	stored.Metadata.UpdatedAt = updatedAt
	return writeJSONAtomic(r.metadataPath(id), stored)
}

func (r *Repository) sessionLock(id agentsession.ID) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock := r.locks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		r.locks[id] = lock
	}
	return lock
}

func (r *Repository) sessionDirectory(id agentsession.ID) string {
	return filepath.Join(r.root, "agent-sessions", string(id))
}

func (r *Repository) metadataPath(id agentsession.ID) string {
	return filepath.Join(r.sessionDirectory(id), "session.json")
}

func (r *Repository) journalPath(id agentsession.ID) string {
	return filepath.Join(r.sessionDirectory(id), "journal.jsonl")
}

func validateSessionID(id agentsession.ID) error {
	if !validID.MatchString(string(id)) {
		return fmt.Errorf("validate agent session id: %q is invalid", id)
	}
	return nil
}

func snapshotLane(snapshot agentsession.Snapshot, lane agentsession.Lane) (agentsession.EntryID, bool) {
	for _, pointer := range snapshot.Lanes {
		if pointer.Lane == lane {
			return pointer.LeafID, true
		}
	}
	return "", false
}

func nextSequence(snapshot agentsession.Snapshot) uint64 {
	if len(snapshot.Log) == 0 {
		return 1
	}
	return snapshot.Log[len(snapshot.Log)-1].Sequence + 1
}

func entryIDExists(snapshot agentsession.Snapshot, id agentsession.EntryID) bool {
	for _, entry := range snapshot.Entries {
		if entry.ID == id {
			return true
		}
	}
	return false
}

func recordIDExists(snapshot agentsession.Snapshot, id agentsession.RecordID) bool {
	for _, record := range snapshot.Records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func randomRecordID() (agentsession.RecordID, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return agentsession.RecordID("record_" + hex.EncodeToString(value)), nil
}

func readJSON(path string, destination any) (bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false, errors.New("multiple JSON values are not allowed")
	}
	return true, nil
}

func writeJSONAtomic(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".codepilot-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readJournal(path string) ([]journalLine, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(content) == 0 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	endsWithNewline := content[len(content)-1] == '\n'
	lines := bytes.Split(content, []byte{'\n'})
	values := make([]journalLine, 0, len(lines))
	for index, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if index == len(lines)-1 && endsWithNewline {
				continue
			}
			return nil, false, fmt.Errorf("empty journal line %d", index+1)
		}
		if len(trimmed) > maxJournalLine {
			return nil, false, fmt.Errorf("journal line %d exceeds size limit", index+1)
		}
		var value journalLine
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&value)
		if decodeErr == nil {
			var extra any
			decodeErr = decoder.Decode(&extra)
			if errors.Is(decodeErr, io.EOF) {
				values = append(values, value)
				continue
			}
		}
		if index == len(lines)-1 && !endsWithNewline {
			return values, true, nil
		}
		return nil, false, fmt.Errorf("decode journal line %d: %w", index+1, decodeErr)
	}
	return values, false, nil
}

func appendJournalLine(path string, value journalLine) error {
	if err := prepareJournal(path); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(encoded)
	if writeErr == nil && written != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func prepareJournal(path string) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(content) == 0 || content[len(content)-1] == '\n' {
		return nil
	}
	if err != nil {
		return err
	}
	lastNewline := bytes.LastIndexByte(content, '\n')
	lineStart := lastNewline + 1
	var value journalLine
	decoder := json.NewDecoder(bytes.NewReader(content[lineStart:]))
	decodeErr := decoder.Decode(&value)
	if decodeErr == nil {
		var extra any
		if errors.Is(decoder.Decode(&extra), io.EOF) {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return err
			}
			_, writeErr := file.Write([]byte{'\n'})
			if writeErr == nil {
				writeErr = file.Sync()
			}
			return errors.Join(writeErr, file.Close())
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	truncateErr := file.Truncate(int64(lineStart))
	if truncateErr == nil {
		truncateErr = file.Sync()
	}
	return errors.Join(truncateErr, file.Close())
}

func cloneEntry(value agentsession.Entry) agentsession.Entry {
	return value.Clone()
}

func cloneRecord(value agentsession.Record) agentsession.Record {
	return value.Clone()
}

func entryPointer(value agentsession.Entry) *agentsession.Entry {
	clone := cloneEntry(value)
	return &clone
}

func recordPointer(value agentsession.Record) *agentsession.Record {
	clone := cloneRecord(value)
	return &clone
}
