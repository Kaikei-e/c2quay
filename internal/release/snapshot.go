package release

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

// psReader is the minimal surface CaptureSnapshot needs from the Compose adapter.
type psReader interface {
	PsJSON(ctx context.Context) ([]composeadapter.ContainerStatus, error)
}

// Snapshot captures the state of a compose project at a point in time.
// Written before and after a deploy so we can diff and generate rollback
// hints when something fails.
type Snapshot struct {
	CapturedAt time.Time                           `json:"captured_at"`
	Env        string                              `json:"env"`
	Releases   map[string]versioning.Release       `json:"releases,omitempty"`
	Containers []composeadapter.ContainerStatus    `json:"containers"`
}

func CaptureSnapshot(ctx context.Context, adapter psReader, env string, releases map[string]versioning.Release) (*Snapshot, error) {
	cs, err := adapter.PsJSON(ctx)
	if err != nil {
		return nil, fmt.Errorf("capture ps: %w", err)
	}
	return &Snapshot{
		CapturedAt: time.Now().UTC(),
		Env:        env,
		Releases:   releases,
		Containers: cs,
	}, nil
}

// Write serializes the snapshot as JSON Lines-friendly single JSON object into dir.
// The filename contains the timestamp and suffix.
func (s *Snapshot) Write(dir, suffix string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir snapshots: %w", err)
	}
	name := fmt.Sprintf("%s-%s.json", s.CapturedAt.Format("20060102T150405Z"), suffix)
	p := filepath.Join(dir, name)
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return "", err
	}
	return p, nil
}
