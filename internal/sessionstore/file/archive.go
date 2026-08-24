package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

const journalArchiveCodec = "tar+gzip"

type journalArchiveManifest struct {
	Version   int                            `json:"version"`
	Reference agentsession.JournalArchiveRef `json:"reference"`
}

// CreateJournalArchive writes a deterministic, content-addressed cold copy.
// It validates but never truncates, rotates, or deletes the live journal.
func (r *Repository) CreateJournalArchive(ctx context.Context, id agentsession.ID) (agentsession.JournalArchiveRef, error) {
	if err := ctx.Err(); err != nil {
		return agentsession.JournalArchiveRef{}, err
	}
	if err := validateSessionID(id); err != nil {
		return agentsession.JournalArchiveRef{}, err
	}
	lock := r.sessionLock(id)
	lock.Lock()
	defer lock.Unlock()
	snapshot, err := r.loadLocked(id)
	if err != nil {
		return agentsession.JournalArchiveRef{}, fmt.Errorf("archive file session %q: %w", id, err)
	}
	metadata, err := os.ReadFile(r.metadataPath(id))
	if err != nil {
		return agentsession.JournalArchiveRef{}, fmt.Errorf("archive file session %q metadata: %w", id, err)
	}
	journal, err := os.ReadFile(r.journalPath(id))
	if errors.Is(err, os.ErrNotExist) {
		journal = nil
	} else if err != nil {
		return agentsession.JournalArchiveRef{}, fmt.Errorf("archive file session %q journal: %w", id, err)
	}
	bundle, err := encodeJournalArchive(metadata, journal)
	if err != nil {
		return agentsession.JournalArchiveRef{}, fmt.Errorf("archive file session %q: %w", id, err)
	}
	digest := sha256.Sum256(bundle)
	digestText := hex.EncodeToString(digest[:])
	lastSequence := uint64(0)
	if len(snapshot.Log) != 0 {
		lastSequence = snapshot.Log[len(snapshot.Log)-1].Sequence
	}
	reference := agentsession.JournalArchiveRef{
		ID: "sha256:" + digestText, SessionID: id, CreatedAt: time.Now().UTC(), Size: int64(len(bundle)), LastSequence: lastSequence, Codec: journalArchiveCodec,
	}
	directory := r.archiveDirectory(id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return agentsession.JournalArchiveRef{}, fmt.Errorf("archive file session %q: create directory: %w", id, err)
	}
	bundlePath := filepath.Join(directory, digestText+".tar.gz")
	if existing, statErr := os.Stat(bundlePath); statErr == nil {
		if !existing.Mode().IsRegular() || existing.Size() != int64(len(bundle)) {
			return agentsession.JournalArchiveRef{}, errors.New("archive file session: existing content-addressed bundle is invalid")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return agentsession.JournalArchiveRef{}, fmt.Errorf("archive file session: inspect bundle: %w", statErr)
	} else if err := writePrivateBytesAtomic(bundlePath, bundle); err != nil {
		return agentsession.JournalArchiveRef{}, fmt.Errorf("archive file session: save bundle: %w", err)
	}
	manifestPath := filepath.Join(directory, digestText+".json")
	if existing, found, _ := r.readArchiveManifest(manifestPath); found {
		reference.CreatedAt = existing.Reference.CreatedAt
		return reference, nil
	}
	if err := writeJSONAtomic(manifestPath, journalArchiveManifest{Version: 1, Reference: reference}); err != nil {
		return agentsession.JournalArchiveRef{}, fmt.Errorf("archive file session: save manifest: %w", err)
	}
	return reference, nil
}

// ListJournalArchives returns stable creation ordering for one session.
func (r *Repository) ListJournalArchives(ctx context.Context, id agentsession.ID) ([]agentsession.JournalArchiveRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(r.archiveDirectory(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list file session archives: %w", err)
	}
	var references []agentsession.JournalArchiveRef
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		manifest, found, err := r.readArchiveManifest(filepath.Join(r.archiveDirectory(id), entry.Name()))
		if err != nil || !found || manifest.Version != 1 || manifest.Reference.SessionID != id {
			return nil, fmt.Errorf("list file session archives: invalid manifest %q", entry.Name())
		}
		references = append(references, manifest.Reference)
	}
	sort.Slice(references, func(left, right int) bool {
		if references[left].CreatedAt.Equal(references[right].CreatedAt) {
			return references[left].ID < references[right].ID
		}
		return references[left].CreatedAt.Before(references[right].CreatedAt)
	})
	return references, nil
}

// LoadJournalArchive verifies the content digest before extracting exact files.
func (r *Repository) LoadJournalArchive(ctx context.Context, reference agentsession.JournalArchiveRef) (agentsession.JournalArchive, error) {
	if err := ctx.Err(); err != nil {
		return agentsession.JournalArchive{}, err
	}
	if err := validateSessionID(reference.SessionID); err != nil {
		return agentsession.JournalArchive{}, err
	}
	digestText := strings.TrimPrefix(reference.ID, "sha256:")
	if len(digestText) != sha256.Size*2 || reference.ID != "sha256:"+digestText || reference.Codec != journalArchiveCodec || reference.Size <= 0 {
		return agentsession.JournalArchive{}, errors.New("load file session archive: invalid reference")
	}
	bundle, err := os.ReadFile(filepath.Join(r.archiveDirectory(reference.SessionID), digestText+".tar.gz"))
	if err != nil {
		return agentsession.JournalArchive{}, fmt.Errorf("load file session archive: %w", err)
	}
	digest := sha256.Sum256(bundle)
	if int64(len(bundle)) != reference.Size || hex.EncodeToString(digest[:]) != digestText {
		return agentsession.JournalArchive{}, errors.New("load file session archive: content verification failed")
	}
	return decodeJournalArchive(bundle)
}

func encodeJournalArchive(metadata, journal []byte) ([]byte, error) {
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	compressed.Header.ModTime = time.Unix(0, 0).UTC()
	compressed.Header.OS = 255
	archive := tar.NewWriter(compressed)
	for _, file := range []struct {
		name string
		data []byte
	}{{"session.json", metadata}, {"journal.jsonl", journal}} {
		header := &tar.Header{Name: file.name, Mode: 0o600, Size: int64(len(file.data)), ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR}
		if err := archive.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := archive.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := compressed.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeJournalArchive(bundle []byte) (agentsession.JournalArchive, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		return agentsession.JournalArchive{}, err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	var result agentsession.JournalArchive
	seen := make(map[string]bool)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return agentsession.JournalArchive{}, err
		}
		if header.Size < 0 || header.Size > 1<<30 || seen[header.Name] {
			return agentsession.JournalArchive{}, errors.New("load file session archive: invalid member")
		}
		data, err := io.ReadAll(io.LimitReader(archive, header.Size+1))
		if err != nil || int64(len(data)) != header.Size {
			return agentsession.JournalArchive{}, errors.New("load file session archive: invalid member data")
		}
		seen[header.Name] = true
		switch header.Name {
		case "session.json":
			result.Metadata = data
		case "journal.jsonl":
			result.Journal = data
		default:
			return agentsession.JournalArchive{}, errors.New("load file session archive: unexpected member")
		}
	}
	if !seen["session.json"] || !seen["journal.jsonl"] {
		return agentsession.JournalArchive{}, errors.New("load file session archive: required members are missing")
	}
	return result, nil
}

func (r *Repository) archiveDirectory(id agentsession.ID) string {
	return filepath.Join(r.root, "agent-archives", string(id))
}

func (r *Repository) readArchiveManifest(path string) (journalArchiveManifest, bool, error) {
	var manifest journalArchiveManifest
	found, err := readJSON(path, &manifest)
	return manifest, found, err
}

func writePrivateBytesAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".archive-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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

var _ agentsession.JournalArchiver = (*Repository)(nil)
