package release_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/release"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

type fakeServiceLister struct {
	services []string
	err      error
	calls    int
}

func (f *fakeServiceLister) ConfigServices(context.Context) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.services, nil
}

func planWithGateOnly(t *testing.T) *release.Plan {
	t.Helper()
	plan, err := release.BuildPlan(context.Background(), &config.Config{
		Environments: map[string]config.Environment{
			"production": {Services: map[string]config.ServiceMapping{
				"api":         {Pacticipant: "api"},
				"tts-speaker": {Pacticipant: "tts-speaker", GateOnly: true},
			}},
		},
	}, "production", "", &fakeStrategy{out: map[string]versioning.Release{
		"api": {Version: "v1"}, "tts-speaker": {Version: "v1"},
	}})
	require.NoError(t, err)
	return plan
}

func eventsContain(events []string, substr string) bool {
	for _, e := range events {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

// TestValidateComposeCoverage_Happy proves the common case: every
// non-gate_only mapped service exists in the resolved Compose config, and
// the gate_only service is correctly absent from it. No error, no warning.
func TestValidateComposeCoverage_Happy(t *testing.T) {
	lister := &fakeServiceLister{services: []string{"api"}}
	ui := &silentUI{}
	err := release.ValidateComposeCoverage(context.Background(), lister, planWithGateOnly(t), false, ui)
	require.NoError(t, err)
	assert.Equal(t, 1, lister.calls)
	assert.False(t, eventsContain(ui.events, "warn:"), "no warning expected in the happy path, got %v", ui.events)
}

// TestValidateComposeCoverage_MissingService proves the exact incident this
// ADR fixes: a mapped, non-gate_only service absent from Compose fails fast
// with a clear, actionable message — before any broker call.
func TestValidateComposeCoverage_MissingService(t *testing.T) {
	lister := &fakeServiceLister{services: []string{}} // "api" is missing entirely
	err := release.ValidateComposeCoverage(context.Background(), lister, planWithGateOnly(t), false, &silentUI{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"api"`)
	assert.Contains(t, err.Error(), "environments.production")
	assert.Contains(t, err.Error(), "gate_only: true")
}

// TestValidateComposeCoverage_MisconfiguredGateOnly proves that a gate_only
// service which DOES exist in Compose is a warning, not an error — it is
// probably a misconfiguration (the service was added to compose.yaml but
// the mapping was never updated), but not a reason to block the deploy.
func TestValidateComposeCoverage_MisconfiguredGateOnly(t *testing.T) {
	lister := &fakeServiceLister{services: []string{"api", "tts-speaker"}}
	ui := &silentUI{}
	err := release.ValidateComposeCoverage(context.Background(), lister, planWithGateOnly(t), false, ui)
	require.NoError(t, err)
	assert.True(t, eventsContain(ui.events, "tts-speaker") && eventsContain(ui.events, "warn:"),
		"expected a warning about the misconfigured gate_only service, got events=%v", ui.events)
}

// TestValidateComposeCoverage_ConfigServicesError_NonDryRun proves that a
// failure resolving the Compose service list is a hard error outside
// dry-run — no silent fallback.
func TestValidateComposeCoverage_ConfigServicesError_NonDryRun(t *testing.T) {
	lister := &fakeServiceLister{err: errors.New("docker: command not found")}
	err := release.ValidateComposeCoverage(context.Background(), lister, planWithGateOnly(t), false, &silentUI{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker: command not found")
}

// TestValidateComposeCoverage_ConfigServicesError_DryRun proves that
// --dry-run tolerates an unreachable Compose (e.g. planning from a box
// without Docker), but must not do so silently — it warns via UI.
func TestValidateComposeCoverage_ConfigServicesError_DryRun(t *testing.T) {
	lister := &fakeServiceLister{err: errors.New("docker: command not found")}
	ui := &silentUI{}
	err := release.ValidateComposeCoverage(context.Background(), lister, planWithGateOnly(t), true, ui)
	require.NoError(t, err)
	assert.True(t, eventsContain(ui.events, "warn:"), "expected a warning when skipping validation in dry-run, got events=%v", ui.events)
}
