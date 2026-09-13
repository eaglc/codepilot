package file

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/eaglc/codepilot/internal/workflow"
)

type workflowJournalRecord struct {
	Version          int            `json:"version"`
	ExpectedRevision uint64         `json:"expected_revision"`
	Event            workflow.Event `json:"event"`
}

// CreateWorkflow writes immutable initial metadata before state events begin.
func (r *Repository) CreateWorkflow(ctx context.Context, value workflow.Workflow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := workflow.Validate(value); err != nil {
		return fmt.Errorf("create Coding workflow: %w", err)
	}
	if value.Status != workflow.StatusPending || value.Revision != 1 {
		return errors.New("create Coding workflow: initial state must be pending revision 1")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	directory, err := r.workflowDirectory(value.ID)
	if err != nil {
		return err
	}
	metadata := filepath.Join(directory, "metadata.json")
	if _, found, err := readEnvelope[workflow.Workflow](metadata); err != nil {
		return fmt.Errorf("create Coding workflow %q: inspect metadata: %w", value.ID, err)
	} else if found {
		return fmt.Errorf("create Coding workflow %q: already exists", value.ID)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Coding workflow %q directory: %w", value.ID, err)
	}
	if err := writeEnvelope(metadata, workflow.Clone(value)); err != nil {
		return fmt.Errorf("create Coding workflow %q metadata: %w", value.ID, err)
	}
	return nil
}

// LoadWorkflow replays the append-only event journal over immutable metadata.
func (r *Repository) LoadWorkflow(ctx context.Context, id workflow.ID) (workflow.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Workflow{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, _, err := r.loadWorkflowLocked(ctx, id)
	return value, err
}

// ListWorkflows returns durable Workflow projections in stable creation order.
func (r *Repository) ListWorkflows(ctx context.Context, ownerID string) ([]workflow.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	directory := filepath.Join(r.root, "coding-workflows")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list Coding workflows: %w", err)
	}
	values := make([]workflow.Workflow, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !validID.MatchString(entry.Name()) {
			continue
		}
		value, _, loadErr := r.loadWorkflowLocked(ctx, workflow.ID(entry.Name()))
		if loadErr != nil {
			return nil, loadErr
		}
		if ownerID == "" || value.OwnerID == ownerID {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].CreatedAt.Equal(values[right].CreatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].CreatedAt.Before(values[right].CreatedAt)
	})
	return values, nil
}

// AppendWorkflowEvent durably appends one state transition using revision CAS.
func (r *Repository) AppendWorkflowEvent(ctx context.Context, id workflow.ID, expectedRevision uint64, event workflow.Event) (workflow.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Workflow{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, events, err := r.loadWorkflowLocked(ctx, id)
	if err != nil {
		return workflow.Workflow{}, err
	}
	if previous, duplicate := events[event.ID]; duplicate {
		if !reflect.DeepEqual(previous, event) {
			return workflow.Workflow{}, fmt.Errorf("append Coding workflow event %q: event id %q changed", id, event.ID)
		}
		return current, nil
	}
	if current.Revision != expectedRevision {
		return workflow.Workflow{}, fmt.Errorf("append Coding workflow event %q: expected revision %d, found %d: %w", id, expectedRevision, current.Revision, workflow.ErrConflict)
	}
	next, err := workflow.ApplyEvent(current, event)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("append Coding workflow event %q: %w", id, err)
	}
	if err := r.appendWorkflowRecord(id, workflowJournalRecord{Version: formatVersion, ExpectedRevision: expectedRevision, Event: event}); err != nil {
		return workflow.Workflow{}, err
	}
	return next, nil
}

func (r *Repository) loadWorkflowLocked(ctx context.Context, id workflow.ID) (workflow.Workflow, map[string]workflow.Event, error) {
	directory, err := r.workflowDirectory(id)
	if err != nil {
		return workflow.Workflow{}, nil, err
	}
	value, found, err := readEnvelope[workflow.Workflow](filepath.Join(directory, "metadata.json"))
	if err != nil {
		return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q metadata: %w", id, err)
	}
	if !found {
		return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q: %w", id, workflow.ErrNotFound)
	}
	if value.ID != id || value.Revision != 1 || value.Status != workflow.StatusPending {
		return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q: initial metadata is inconsistent", id)
	}
	if err := workflow.Validate(value); err != nil {
		return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q: invalid metadata: %w", id, err)
	}
	events := make(map[string]workflow.Event)
	journal := filepath.Join(directory, "events.jsonl")
	file, err := os.Open(journal)
	if errors.Is(err, os.ErrNotExist) {
		return value, events, nil
	}
	if err != nil {
		return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q events: %w", id, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		var record workflowJournalRecord
		if err := json.Unmarshal(line, &record); err != nil {
			if scanner.Scan() {
				return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q event line %d: %w", id, lineNumber, err)
			}
			return value, events, nil
		}
		if record.Version != formatVersion || record.ExpectedRevision != value.Revision {
			return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q event line %d: revision is inconsistent", id, lineNumber)
		}
		if _, duplicate := events[record.Event.ID]; duplicate {
			return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q event line %d: duplicate event id", id, lineNumber)
		}
		next, applyErr := workflow.ApplyEvent(value, record.Event)
		if applyErr != nil {
			return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q event line %d: %w", id, lineNumber, applyErr)
		}
		events[record.Event.ID] = record.Event
		value = next
	}
	if err := scanner.Err(); err != nil {
		return workflow.Workflow{}, nil, fmt.Errorf("load Coding workflow %q events: %w", id, err)
	}
	return value, events, nil
}

func (r *Repository) appendWorkflowRecord(id workflow.ID, record workflowJournalRecord) error {
	directory, err := r.workflowDirectory(id)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "events.jsonl")
	if err := repairWorkflowJournalTail(path); err != nil {
		return fmt.Errorf("repair Coding workflow event tail: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("append Coding workflow event: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		_ = file.Close()
		return fmt.Errorf("append Coding workflow event: encode: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("append Coding workflow event: sync: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("append Coding workflow event: close: %w", err)
	}
	return nil
}

func repairWorkflowJournalTail(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	size := info.Size()
	last := []byte{0}
	if _, err := file.ReadAt(last, size-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	const maximumRecordSize = int64(2 << 20)
	window := min(size, maximumRecordSize)
	buffer := make([]byte, int(window))
	if _, err := file.ReadAt(buffer, size-window); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	lineStart := int64(0)
	if index := bytes.LastIndexByte(buffer, '\n'); index >= 0 {
		lineStart = size - window + int64(index+1)
	} else if size > window {
		return errors.New("final Workflow event exceeds its size limit")
	}
	tail := buffer[int(lineStart-(size-window)):]
	var record workflowJournalRecord
	if json.Unmarshal(tail, &record) == nil {
		if _, err := file.WriteAt([]byte{'\n'}, size); err != nil {
			return err
		}
	} else if err := file.Truncate(lineStart); err != nil {
		return err
	}
	return file.Sync()
}

func (r *Repository) workflowDirectory(id workflow.ID) (string, error) {
	if !validID.MatchString(string(id)) {
		return "", fmt.Errorf("Coding workflow id %q is invalid", id)
	}
	return filepath.Join(r.root, "coding-workflows", string(id)), nil
}
