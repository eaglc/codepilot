package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/eaglc/codepilot/internal/contextmanager"
)

var validSummaryKey = regexp.MustCompile(`^[a-f0-9]{64}$`)

type summaryFile struct {
	Version int                    `json:"version"`
	Summary contextmanager.Summary `json:"summary"`
}

// LoadSummary implements contextmanager.SummaryStore using a versioned durable cache.
func (r *Repository) LoadSummary(ctx context.Context, key string) (contextmanager.Summary, bool, error) {
	if err := ctx.Err(); err != nil {
		return contextmanager.Summary{}, false, err
	}
	if !validSummaryKey.MatchString(key) {
		return contextmanager.Summary{}, false, fmt.Errorf("load file summary: invalid key")
	}
	var stored summaryFile
	found, err := readJSON(r.summaryPath(key), &stored)
	if err != nil {
		return contextmanager.Summary{}, false, fmt.Errorf("load file summary: %w", err)
	}
	if !found {
		return contextmanager.Summary{}, false, nil
	}
	if stored.Version != 1 || stored.Summary.SourceDigest == "" || stored.Summary.Strategy == "" || stored.Summary.StrategyVersion == "" || stored.Summary.Text == "" {
		return contextmanager.Summary{}, false, fmt.Errorf("load file summary: stored summary is invalid")
	}
	return stored.Summary, true, nil
}

// SaveSummary implements contextmanager.SummaryStore using an atomic replace.
func (r *Repository) SaveSummary(ctx context.Context, key string, summary contextmanager.Summary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSummaryKey.MatchString(key) {
		return fmt.Errorf("save file summary: invalid key")
	}
	if summary.SourceDigest == "" || summary.Strategy == "" || summary.StrategyVersion == "" || summary.Text == "" {
		return errors.New("save file summary: summary metadata is incomplete")
	}
	if err := os.MkdirAll(filepath.Dir(r.summaryPath(key)), 0o700); err != nil {
		return fmt.Errorf("save file summary: create directory: %w", err)
	}
	if err := writeJSONAtomic(r.summaryPath(key), summaryFile{Version: 1, Summary: summary}); err != nil {
		return fmt.Errorf("save file summary: %w", err)
	}
	return nil
}

func (r *Repository) summaryPath(key string) string {
	return filepath.Join(r.root, "context-summaries", key+".json")
}
