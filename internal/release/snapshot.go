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

// snapshotSource is the minimal surface CaptureSnapshot needs from the
// Compose adapter: current container states plus the resolved image-per-service
// map (for rollback plans).
type snapshotSource interface {
	PsJSON(ctx context.Context) ([]composeadapter.ContainerStatus, error)
	RenderConfigJSON(ctx context.Context) (*composeadapter.RenderedConfig, error)
}

// Snapshot captures the state of a compose project at a point in time.
// Written before and after a deploy so we can diff, generate rollback hints,
// and execute auto-rollback when something fails.
type Snapshot struct {
	CapturedAt time.Time                        `json:"captured_at"`
	Env        string                           `json:"env"`
	Releases   map[string]versioning.Release    `json:"releases,omitempty"`
	Containers []composeadapter.ContainerStatus `json:"containers"`
	// Images maps service name → resolved image reference at capture time.
	// Populated from `docker compose config --format json`. May be empty if
	// the render call fails; callers treat empty Images as "rollback not
	// possible from this snapshot".
	Images map[string]string `json:"images,omitempty"`
}

func CaptureSnapshot(ctx context.Context, adapter snapshotSource, env string, releases map[string]versioning.Release) (*Snapshot, error) {
	cs, err := adapter.PsJSON(ctx)
	if err != nil {
		return nil, fmt.Errorf("capture ps: %w", err)
	}
	snap := &Snapshot{
		CapturedAt: time.Now().UTC(),
		Env:        env,
		Releases:   releases,
		Containers: cs,
	}
	// Image capture is best-effort: a failure here must not sink the whole
	// deploy. The rollback flow later inspects Images and skips if empty.
	if rc, rerr := adapter.RenderConfigJSON(ctx); rerr == nil && rc != nil {
		snap.Images = rc.ImagesByService()
	}
	return snap, nil
}

// Write serializes the snapshot as JSON Lines-friendly single JSON object into dir.
// The filename contains the timestamp and suffix.
func (s *Snapshot) Write(dir, suffix string) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
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
