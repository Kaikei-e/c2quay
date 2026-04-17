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
	canIDeployFunc       func(ctx context.Context, in broker.CanIDeployInput) (*broker.CanIDeployResult, error)
	canIDeployManyFunc   func(ctx context.Context, env string, selectors []broker.CanIDeploySelector) (*broker.CanIDeploySetResult, error)
	recordDeploymentFunc func(ctx context.Context, in broker.RecordDeploymentInput) error
	hasRelationFunc      func(rel string) bool
	apiCalls             int
	recordCalls          []broker.RecordDeploymentInput
}

func (f *fakeDeployBroker) CanIDeploy(ctx context.Context, in broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
	f.apiCalls++
	return f.canIDeployFunc(ctx, in)
}
func (f *fakeDeployBroker) CanIDeployMany(ctx context.Context, env string, selectors []broker.CanIDeploySelector) (*broker.CanIDeploySetResult, error) {
	f.apiCalls++
	if f.canIDeployManyFunc != nil {
		return f.canIDeployManyFunc(ctx, env, selectors)
	}
	return nil, errors.New("fakeDeployBroker: CanIDeployMany not configured")
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
	upErr     error
	upCalls   []composeadapter.UpOptions
	pullCalls [][]string
	pullErr   error
	ps        []composeadapter.ContainerStatus
	psErr     error
	renderCfg *composeadapter.RenderedConfig
	renderErr error
	// renderCfgs lets a test script successive RenderConfigJSON responses.
	// If set, each call pops the next entry; calls past the end fall back to
	// the last value or renderCfg.
	renderCfgs []*composeadapter.RenderedConfig
	renderIdx  int
}

func (f *fakeCompose) Pull(_ context.Context, services []string, _ io.Writer) error {
	svcCopy := append([]string(nil), services...)
	f.pullCalls = append(f.pullCalls, svcCopy)
	return f.pullErr
}

func (f *fakeCompose) Up(_ context.Context, opts composeadapter.UpOptions, _ io.Writer) error {
	f.upCalls = append(f.upCalls, opts)
	return f.upErr
}
func (f *fakeCompose) PsJSON(context.Context) ([]composeadapter.ContainerStatus, error) {
	return f.ps, f.psErr
}
func (f *fakeCompose) RenderConfigJSON(context.Context) (*composeadapter.RenderedConfig, error) {
	if f.renderErr != nil {
		return nil, f.renderErr
	}
	if len(f.renderCfgs) > 0 {
		idx := f.renderIdx
		if idx >= len(f.renderCfgs) {
			idx = len(f.renderCfgs) - 1
		}
		f.renderIdx++
		return f.renderCfgs[idx], nil
	}
	if f.renderCfg != nil {
		return f.renderCfg, nil
	}
	return &composeadapter.RenderedConfig{Services: map[string]composeadapter.RenderedService{}}, nil
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

// TestDeploy_AutoRollbackOnComposeUpFailure proves that when compose-up fails
// and RollbackMode is on (the default), the rollback flow is invoked — the
// fake compose records a second Up call with ExtraFiles pointing at the
// generated override.
func TestDeploy_AutoRollbackOnComposeUpFailure(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	// Pre-snapshot sees v1 (what's currently deployed); post-failure capture
	// and the rollback executor see v2 (user's new compose). The plan builder
	// notices the mismatch and triggers a rollback.
	cp := &fakeCompose{
		upErr: errors.New("compose boom"),
		renderCfgs: []*composeadapter.RenderedConfig{
			{Services: map[string]composeadapter.RenderedService{"api": {Image: "api:v1"}}},
			{Services: map[string]composeadapter.RenderedService{"api": {Image: "api:v2"}}},
		},
	}
	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()
	deps.RollbackMode = release.RollbackOn

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.Error(t, err)
	require.NotNil(t, report)
	assert.Equal(t, release.StepComposeUp, report.FailedAtStep)
	assert.NotNil(t, report.Pre)
	assert.Equal(t, "api:v1", report.Pre.Images["api"])
	require.NotNil(t, report.Rollback, "auto-rollback report should be populated")
	assert.True(t, report.Rollback.Attempted)

	// Two Up calls total: the failing deploy Up + the rollback Up (which
	// also fails because the fake always returns upErr; that's fine — we
	// just want to assert rollback was attempted with the override).
	require.GreaterOrEqual(t, len(cp.upCalls), 2, "rollback should have attempted a compose up")
	rbCall := cp.upCalls[len(cp.upCalls)-1]
	require.Len(t, rbCall.ExtraFiles, 1, "rollback Up must pass an override file")
	assert.Contains(t, rbCall.ExtraFiles[0], "override.yml")
}

// TestDeploy_AutoRollbackOffPreservesOldBehaviour verifies that with
// RollbackMode=off, no second Up call fires and the report has no Rollback.
func TestDeploy_AutoRollbackOffPreservesOldBehaviour(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{upErr: errors.New("compose boom")}
	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()
	deps.RollbackMode = release.RollbackOff

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.Error(t, err)
	require.NotNil(t, report)
	assert.Equal(t, release.StepComposeUp, report.FailedAtStep)
	assert.Nil(t, report.Rollback, "no rollback attempted when mode=off")
	assert.Len(t, cp.upCalls, 1, "only the initial deploy Up should run when rollback is off")
}

// TestDeploy_RecordFailureNoAutoRollback confirms that a record-deployment
// failure never triggers auto-rollback (PolicyFor(StepRecord)==false).
func TestDeploy_RecordFailureNoAutoRollback(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
		recordDeploymentFunc: func(_ context.Context, _ broker.RecordDeploymentInput) error {
			return errors.New("broker 503")
		},
	}
	cp := &fakeCompose{ps: []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}}}
	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()
	deps.RollbackMode = release.RollbackOn // even with on, StepRecord skips

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.Error(t, err)
	require.NotNil(t, report)
	assert.Equal(t, release.StepRecord, report.FailedAtStep)
	assert.Nil(t, report.Rollback, "record-deployment failure must NOT auto-rollback")
	assert.Len(t, cp.upCalls, 1, "only the initial deploy Up should run")
}

// TestDeploy_PullAlwaysFiresBetweenGateAndUp proves ADR 0010: when
// cfg.Deploy.Pull == "always", Pull runs after the gate and before compose up.
func TestDeploy_PullAlwaysFiresBetweenGateAndUp(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true, Reason: "ok"}, nil
		},
	}
	cp := &fakeCompose{ps: []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}}}

	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	cfg := baseConfig()
	cfg.Deploy.Pull = "always"

	report, err := release.Deploy(context.Background(), cfg, "production", "", false, deps)
	require.NoError(t, err)
	require.NotNil(t, report)
	require.Len(t, cp.pullCalls, 1, "pull should fire exactly once")
	assert.Equal(t, []string{"api"}, cp.pullCalls[0])
	require.Len(t, cp.upCalls, 1, "compose up should still fire after pull")
}

// TestDeploy_PullNeverDoesNotPull proves the default path is unchanged.
func TestDeploy_PullNeverDoesNotPull(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{ps: []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}}}

	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	cfg := baseConfig()
	// Pull left as zero-value ("") — pipeline treats anything != "always" as no-op.

	_, err := release.Deploy(context.Background(), cfg, "production", "", false, deps)
	require.NoError(t, err)
	assert.Empty(t, cp.pullCalls, "pull must not fire when deploy.pull != always")
}

// TestDeploy_PullFailureBlocksUp proves that a pull error aborts the deploy
// before compose up is attempted (we never want to ship stale images because
// pull silently failed).
func TestDeploy_PullFailureBlocksUp(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{pullErr: errors.New("registry 500")}

	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()
	deps.RollbackMode = release.RollbackOff

	cfg := baseConfig()
	cfg.Deploy.Pull = "always"

	_, err := release.Deploy(context.Background(), cfg, "production", "", false, deps)
	require.Error(t, err)
	assert.Empty(t, cp.upCalls, "compose up must not run when pull fails")
	assert.Empty(t, bk.recordCalls, "record-deployment must not run when pull fails")
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
