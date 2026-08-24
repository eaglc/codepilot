package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const maxArtifactBytes = 32 << 20

// SaveArtifact stores immutable content under its SHA-256 digest with private
// permissions. Re-saving identical data returns the same reference.
func (r *Repository) SaveArtifact(ctx context.Context, artifact codingagent.Artifact) (codingagent.ArtifactRef, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.ArtifactRef{}, err
	}
	if r == nil {
		return codingagent.ArtifactRef{}, errors.New("save Coding artifact: repository is nil")
	}
	mediaType := strings.TrimSpace(artifact.MediaType)
	if mediaType == "" || len(mediaType) > 256 || strings.ContainsAny(mediaType, "\r\n\x00") {
		return codingagent.ArtifactRef{}, errors.New("save Coding artifact: media type is invalid")
	}
	if len(artifact.Data) == 0 || len(artifact.Data) > maxArtifactBytes {
		return codingagent.ArtifactRef{}, fmt.Errorf("save Coding artifact: data must be between 1 and %d bytes", maxArtifactBytes)
	}
	digest := sha256.Sum256(artifact.Data)
	hexDigest := hex.EncodeToString(digest[:])
	reference := codingagent.ArtifactRef{ID: "sha256:" + hexDigest, MediaType: mediaType, Size: int64(len(artifact.Data))}
	directory := filepath.Join(r.root, "coding-artifacts")
	path := filepath.Join(directory, hexDigest+".blob")
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, err := os.Stat(path); err == nil {
		if info.Mode().IsRegular() && info.Size() == reference.Size {
			return reference, nil
		}
		return codingagent.ArtifactRef{}, errors.New("save Coding artifact: existing content-addressed file is invalid")
	} else if !errors.Is(err, os.ErrNotExist) {
		return codingagent.ArtifactRef{}, fmt.Errorf("save Coding artifact: inspect existing file: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".artifact-*.tmp")
	if err != nil {
		return codingagent.ArtifactRef{}, fmt.Errorf("save Coding artifact: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return codingagent.ArtifactRef{}, fmt.Errorf("save Coding artifact: secure temporary file: %w", err)
	}
	if _, err := temporary.Write(artifact.Data); err != nil {
		temporary.Close()
		return codingagent.ArtifactRef{}, fmt.Errorf("save Coding artifact: write: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return codingagent.ArtifactRef{}, fmt.Errorf("save Coding artifact: sync: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return codingagent.ArtifactRef{}, fmt.Errorf("save Coding artifact: close: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return codingagent.ArtifactRef{}, fmt.Errorf("save Coding artifact: commit: %w", err)
	}
	return reference, nil
}

// LoadArtifact verifies the reference, size, and digest before returning data.
func (r *Repository) LoadArtifact(ctx context.Context, reference codingagent.ArtifactRef) (codingagent.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Artifact{}, err
	}
	if r == nil {
		return codingagent.Artifact{}, errors.New("load Coding artifact: repository is nil")
	}
	digestText := strings.TrimPrefix(reference.ID, "sha256:")
	if len(digestText) != sha256.Size*2 || "sha256:"+digestText != reference.ID {
		return codingagent.Artifact{}, errors.New("load Coding artifact: reference id is invalid")
	}
	digest, err := hex.DecodeString(digestText)
	if err != nil {
		return codingagent.Artifact{}, errors.New("load Coding artifact: reference id is invalid")
	}
	if reference.Size <= 0 || reference.Size > maxArtifactBytes {
		return codingagent.Artifact{}, errors.New("load Coding artifact: reference size is invalid")
	}
	path := filepath.Join(r.root, "coding-artifacts", digestText+".blob")
	data, err := os.ReadFile(path)
	if err != nil {
		return codingagent.Artifact{}, fmt.Errorf("load Coding artifact: read: %w", err)
	}
	actual := sha256.Sum256(data)
	if int64(len(data)) != reference.Size || !strings.EqualFold(hex.EncodeToString(actual[:]), hex.EncodeToString(digest)) {
		return codingagent.Artifact{}, errors.New("load Coding artifact: content verification failed")
	}
	return codingagent.Artifact{MediaType: reference.MediaType, Data: data}, nil
}

var _ codingagent.ArtifactStore = (*Repository)(nil)
var _ codingagent.ArtifactReader = (*Repository)(nil)
