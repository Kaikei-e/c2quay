package release_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/release"
)

// snapshotOnlyFake implements just what CaptureSnapshot needs
// (PsJSON + RenderConfigJSON), independent of the fuller fakeCompose used
// elsewhere, to keep this test file's intent narrow and obvious.
type snapshotOnlyFake struct {
	ps        []composeadapter.ContainerStatus
	psErr     error
	renderCfg *composeadapter.RenderedConfig
	renderErr error
}

func (f *snapshotOnlyFake) PsJSON(context.Context) ([]composeadapter.ContainerStatus, error) {
	return f.ps, f.psErr
}

func (f *snapshotOnlyFake) RenderConfigJSON(context.Context) (*composeadapter.RenderedConfig, error) {
	if f.renderErr != nil {
		return nil, f.renderErr
	}
	return f.renderCfg, nil
}

// TestCaptureSnapshot_RenderConfigFails_SetsImageCaptureFailed proves the
// fix for the silent-rollback-degradation defect: when
// `docker compose config --format json` fails, the resulting snapshot must
// say so explicitly (not just end up with an empty Images map that looks
// identical to "nothing was ever deployed").
func TestCaptureSnapshot_RenderConfigFails_SetsImageCaptureFailed(t *testing.T) {
	adapter := &snapshotOnlyFake{
		ps:        []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}},
		renderErr: errors.New("docker: daemon not reachable"),
	}
	snap, err := release.CaptureSnapshot(context.Background(), adapter, "production", nil)
	require.NoError(t, err, "a render failure must not fail the whole snapshot capture")
	require.NotNil(t, snap)
	assert.Empty(t, snap.Images)
	assert.True(t, snap.ImageCaptureFailed)
	assert.Contains(t, snap.ImageCaptureFailReason, "docker: daemon not reachable")
}

// TestCaptureSnapshot_RenderConfigSucceeds_NoFailureFlag is the control
// case: a normal, successful capture must not set ImageCaptureFailed, even
// though Images may legitimately be empty (e.g. no services rendered).
func TestCaptureSnapshot_RenderConfigSucceeds_NoFailureFlag(t *testing.T) {
	adapter := &snapshotOnlyFake{
		ps:        []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}},
		renderCfg: &composeadapter.RenderedConfig{Services: map[string]composeadapter.RenderedService{"api": {Image: "api:v1"}}},
	}
	snap, err := release.CaptureSnapshot(context.Background(), adapter, "production", nil)
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, "api:v1", snap.Images["api"])
	assert.False(t, snap.ImageCaptureFailed)
	assert.Empty(t, snap.ImageCaptureFailReason)
}

// TestCaptureSnapshot_PsFails_StillErrorsOut proves ps failures remain a
// hard error (unchanged behaviour) — only the image-render call is
// best-effort.
func TestCaptureSnapshot_PsFails_StillErrorsOut(t *testing.T) {
	adapter := &snapshotOnlyFake{psErr: errors.New("docker: not found")}
	_, err := release.CaptureSnapshot(context.Background(), adapter, "production", nil)
	require.Error(t, err)
}
