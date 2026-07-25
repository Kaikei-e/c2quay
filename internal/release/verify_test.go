package release_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/release"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

// verifyBroker approves every service — these tests are about compose
// coverage, not gate outcomes.
func verifyBroker() *fakeBrokerClient {
	return &fakeBrokerClient{
		responses: map[string]*broker.CanIDeployResult{
			"api|v1|production":         {Deployable: true},
			"tts-speaker|v1|production": {Deployable: true},
		},
	}
}

// TestVerify_NoComposeWired_BackwardCompatible proves the M1 fix does not
// break existing callers: VerifyDeps.Compose is optional, and when nil,
// Verify behaves exactly as before — no coverage check attempted, no
// notice, gate outcomes drive AllPassed/FirstError as always.
func TestVerify_NoComposeWired_BackwardCompatible(t *testing.T) {
	report, err := release.Verify(context.Background(), gateOnlyCfg(), "production", "", release.VerifyDeps{
		Broker:   verifyBroker(),
		Strategy: gateOnlyStrategy(),
	})
	require.NoError(t, err)
	assert.False(t, report.CoverageChecked)
	assert.Empty(t, report.CoverageNotice)
	assert.NoError(t, report.CoverageErr)
	assert.True(t, report.AllPassed())
}

// TestVerify_ComposeCoverage_MissingService_FailsVerify proves the core M1
// fix: verify now catches the exact gap deploy's ValidateComposeCoverage
// catches (a non-gate_only mapped service missing from compose), instead of
// passing verify and only discovering the problem at deploy's hard `compose
// up` failure.
func TestVerify_ComposeCoverage_MissingService_FailsVerify(t *testing.T) {
	lister := &fakeServiceLister{services: []string{}} // "api" missing entirely
	report, err := release.Verify(context.Background(), gateOnlyCfg(), "production", "", release.VerifyDeps{
		Broker:   verifyBroker(),
		Strategy: gateOnlyStrategy(),
		Compose:  lister,
	})
	require.NoError(t, err)
	require.True(t, report.CoverageChecked)
	require.Error(t, report.CoverageErr)
	assert.Contains(t, report.CoverageErr.Error(), `"api"`)
	assert.Contains(t, report.CoverageErr.Error(), "gate_only: true")

	assert.False(t, report.AllPassed(), "a compose coverage failure must make verify report failure")
	assert.Equal(t, report.CoverageErr, report.FirstError())
}

// TestVerify_ComposeCoverage_Happy proves the non-broken path: every
// non-gate_only service is present in compose, coverage is checked, and it
// contributes no failure.
func TestVerify_ComposeCoverage_Happy(t *testing.T) {
	lister := &fakeServiceLister{services: []string{"api"}}
	report, err := release.Verify(context.Background(), gateOnlyCfg(), "production", "", release.VerifyDeps{
		Broker:   verifyBroker(),
		Strategy: gateOnlyStrategy(),
		Compose:  lister,
	})
	require.NoError(t, err)
	assert.True(t, report.CoverageChecked)
	assert.NoError(t, report.CoverageErr)
	assert.True(t, report.AllPassed())
}

// TestVerify_ComposeCoverage_ProbeError_PrintsNoticeAndDegrades proves the
// deliberate, explicit degradation: when the compose CLI probe itself fails
// (e.g. no docker on this box), verify must NOT hard-fail — it must still
// be usable on a broker-only box — but it must also not be silent about
// skipping the check. A clear notice is set, and the gate checks still run
// and still determine AllPassed/FirstError on their own.
func TestVerify_ComposeCoverage_ProbeError_PrintsNoticeAndDegrades(t *testing.T) {
	lister := &fakeServiceLister{err: errors.New("docker: command not found")}
	report, err := release.Verify(context.Background(), gateOnlyCfg(), "production", "", release.VerifyDeps{
		Broker:   verifyBroker(),
		Strategy: gateOnlyStrategy(),
		Compose:  lister,
	})
	require.NoError(t, err)
	assert.False(t, report.CoverageChecked)
	assert.Contains(t, report.CoverageNotice, "compose coverage not checked:")
	assert.Contains(t, report.CoverageNotice, "docker: command not found")
	assert.NoError(t, report.CoverageErr)

	// Gate checks still ran and still drive the verdict — verify degrades to
	// gate-only checks, it doesn't abort.
	require.Len(t, report.Outcomes, 2)
	assert.True(t, report.AllPassed())
}

func gateOnlyCfg() *config.Config {
	return &config.Config{
		Environments: map[string]config.Environment{
			"production": {Services: map[string]config.ServiceMapping{
				"api":         {Pacticipant: "api"},
				"tts-speaker": {Pacticipant: "tts-speaker", GateOnly: true},
			}},
		},
	}
}

func gateOnlyStrategy() *fakeStrategy {
	return &fakeStrategy{out: map[string]versioning.Release{
		"api":         {Version: "v1"},
		"tts-speaker": {Version: "v1"},
	}}
}
