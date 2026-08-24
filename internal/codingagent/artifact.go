package codingagent

import "context"

// Artifact contains bounded product output that is too large for a session
// message. Data may be sensitive and must be stored with private permissions.
type Artifact struct {
	MediaType string
	Data      []byte
}

// ArtifactRef is a content-addressed, path-free durable reference.
type ArtifactRef struct {
	ID        string
	MediaType string
	Size      int64
}

// ArtifactStore persists bounded large outputs outside the Agent journal.
type ArtifactStore interface {
	SaveArtifact(ctx context.Context, artifact Artifact) (ArtifactRef, error)
}

// ArtifactReader resolves a content-addressed reference for audit, explicit
// inspection, or future reprocessing. It is deliberately separate from the
// write capability so tool execution only receives the authority it needs.
type ArtifactReader interface {
	LoadArtifact(ctx context.Context, reference ArtifactRef) (Artifact, error)
}
