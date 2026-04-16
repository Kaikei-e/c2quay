package release_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/release"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

// --- fakes -------------------------------------------------------------

type fakeDeployBroker struct {
	canIDeployFunc      func(ctx context.Context, in broker.CanIDeployInput) (*broker.CanIDeployResult, error)
	recordDeploymentFunc func(ctx context.Context, in broker.RecordDeploymentInput) error
	hasRelationFunc     func(rel string) bool
	apiCalls            int
	recordCalls         []broker.RecordDeploymentInput
}

func (f *fakeDeployBroker) CanIDeploy(ctx context.Context, in broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
	f.apiCalls++
	return f.canIDeployFunc(ctx, in)
}
func (f *fakeDeployBroker) RecordDeployment(ctx context.Context, in broker.RecordDeploymentInput) error {
	f.apiCalls++
	f.recordCalls = append(f.recordCalls, in)
	if f.recordDeploymentFunc != nil {
		return f.recordDeploymentFunc(ctx, in)
	}
	return nil
}
func (f *fakeDeployBroker) HasRelation(rel string) bool {
	if f.hasRelationFunc != nil {
		return f.hasRelationFunc(rel)
	}
	return true
}
func (f *fakeDeployBroker) APICallCount() int { return f.apiCalls }

type fakeCompose struct {
	upErr   error
	upCalls []composeadapter.UpOptions
	ps      []composeadapter.ContainerStatus
	psErr   error
}

func (f *fakeCompose) Up(_ context.Context, opts composeadapter.UpOptions, _ io.Writer) error {
	f.upCalls = append(f.upCalls, opts)
	return f.upErr
}
func (f *fakeCompose) PsJSON(context.Context) ([]composeadapter.ContainerStatus, error) {
	return f.ps, f.psErr
}

type silentUI struct{ events []string }

func (u *silentUI) Step(l, d string) { u.events = append(u.events, "step:"+l+"|"+d) }
func (u *silentUI) Ok(l, d string)   { u.events = append(u.events, "ok:"+l+"|"+d) }
func (u *silentUI) Fail(l, d string) { u.events = append(u.events, "fail:"+l+"|"+d) }
func (u *silentUI) Warn(l, d string) { u.events = append(u.events, "warn:"+l+"|"+d) }

// --- helpers -----------------------------------------------------------

func baseConfig() *config.Config {
	return &config.Config{
		Broker: config.BrokerConfig{BaseURL: "http://broker"},
		Environments: map[string]config.Environment{
			"production": {Services: map[string]config.ServiceMapping{
				"api": {Pacticipant: "api"},
			}},
		},
	}
}

func baseDeps(broker release.DeployBroker, compose release.ComposeDeployer) release.DeployDeps {
	return release.DeployDeps{
		Broker:      broker,
		Compose:     compose,
		Strategy:    &fakeStrategy{out: map[string]versioning.Release{"api": {Version: "v1"}}},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		UI:          &silentUI{},
		Progress:    io.Discard,
		Stderr:      io.Discard,
		SnapshotDir: "",
	}
}

// --- tests -------------------------------------------------------------

func TestDeploy_HappyPath(t *testing.T) {
	tr := true
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true, Reason: "ok"}, nil
		},
	}
	_ = tr
	cp := &fakeCompose{ps: []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}}}

	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Empty(t, report.FailedAtStep)
	assert.Len(t, cp.upCalls, 1, "compose up should run once")
	assert.Len(t, bk.recordCalls, 1, "record-deployment should run once")
	assert.Equal(t, "api", bk.recordCalls[0].Pacticipant)
}

// TestDeploy_GateFailed_NoRecord proves ADR 0004: if the gate fails,
// record-deployment is never called.
func TestDeploy_GateFailed_NoRecord(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: false, Reason: "pact broken"}, nil
		},
	}
	cp := &fakeCompose{}
	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.Error(t, err)
	assert.True(t, errors.Is(err, broker.ErrGateFailed), "gate failure must propagate ErrGateFailed, got %v", err)
	assert.Equal(t, release.StepGate, report.FailedAtStep)
	assert.Empty(t, bk.recordCalls, "record-deployment MUST NOT fire when gate fails")
	assert.Empty(t, cp.upCalls, "compose up MUST NOT fire when gate fails")
}

// TestDeploy_ComposeUpFailed_NoRecord proves record is skipped if compose up fails.
func TestDeploy_ComposeUpFailed_NoRecord(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{upErr: errors.New("boom")}
	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.Error(t, err)
	assert.Equal(t, release.StepComposeUp, report.FailedAtStep)
	assert.Empty(t, bk.recordCalls, "record-deployment MUST NOT fire when compose up fails")
}

func TestDeploy_DryRun_StopsBeforeCompose(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{}
	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", true, deps)
	require.NoError(t, err)
	assert.Empty(t, report.FailedAtStep)
	assert.Empty(t, cp.upCalls, "compose up MUST NOT run in dry-run")
	assert.Empty(t, bk.recordCalls, "record-deployment MUST NOT run in dry-run")
}

func TestDeploy_MissingDeps_Errors(t *testing.T) {
	_, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, release.DeployDeps{})
	require.Error(t, err)

	var buf bytes.Buffer
	deps := baseDeps(&fakeDeployBroker{}, &fakeCompose{})
	deps.UI = nil
	_ = buf
	_, err = release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.Error(t, err)
}
