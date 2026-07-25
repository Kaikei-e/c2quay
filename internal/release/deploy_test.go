package release_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
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

	// mu guards apiCalls/recordCalls: gateIndividual fans CanIDeploy out
	// across a worker pool (see pipeline.go), so this fake must be
	// concurrency-safe even though most tests only ever map one service.
	mu          sync.Mutex
	apiCalls    int
	recordCalls []broker.RecordDeploymentInput
}

func (f *fakeDeployBroker) CanIDeploy(ctx context.Context, in broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
	f.mu.Lock()
	f.apiCalls++
	f.mu.Unlock()
	return f.canIDeployFunc(ctx, in)
}
func (f *fakeDeployBroker) CanIDeployMany(ctx context.Context, env string, selectors []broker.CanIDeploySelector) (*broker.CanIDeploySetResult, error) {
	f.mu.Lock()
	f.apiCalls++
	f.mu.Unlock()
	if f.canIDeployManyFunc != nil {
		return f.canIDeployManyFunc(ctx, env, selectors)
	}
	return nil, errors.New("fakeDeployBroker: CanIDeployMany not configured")
}
func (f *fakeDeployBroker) RecordDeployment(ctx context.Context, in broker.RecordDeploymentInput) error {
	f.mu.Lock()
	f.apiCalls++
	f.recordCalls = append(f.recordCalls, in)
	f.mu.Unlock()
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
func (f *fakeDeployBroker) APICallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apiCalls
}

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

	// configServices backs ConfigServices, used by plan-time gate_only
	// coverage validation (ValidateComposeCoverage). A nil (unset) value
	// defaults to ["api"] so every existing fakeCompose literal in this
	// file — which all map against baseConfig()'s single "api" service —
	// keeps passing validation without being touched. Pass a non-nil slice
	// (empty or otherwise) to exercise gate_only / missing-service paths.
	configServices    []string
	configServicesErr error
	configSvcCalls    int
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

func (f *fakeCompose) ConfigServices(context.Context) ([]string, error) {
	f.configSvcCalls++
	if f.configServicesErr != nil {
		return nil, f.configServicesErr
	}
	if f.configServices == nil {
		return []string{"api"}, nil
	}
	return f.configServices, nil
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

// TestDeploy_ForceRecreateReachesUpOptions proves ADR 0011: the flag is
// plumbed from DeployDeps through to UpOptions.
func TestDeploy_ForceRecreateReachesUpOptions(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{ps: []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}}}

	deps := baseDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()
	deps.ForceRecreate = true

	_, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.NoError(t, err)
	require.Len(t, cp.upCalls, 1)
	assert.True(t, cp.upCalls[0].ForceRecreate, "force-recreate must reach compose UpOptions")
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

// --- gate_only ------------------------------------------------------------
// See ADR 0013: gate_only services are gated and recorded but must never
// reach docker compose, and a missing (non-gate_only) service must fail the
// deploy before the Pact gate runs at all.

func gateOnlyConfig() *config.Config {
	return &config.Config{
		Broker: config.BrokerConfig{BaseURL: "http://broker"},
		Environments: map[string]config.Environment{
			"production": {Services: map[string]config.ServiceMapping{
				"api":         {Pacticipant: "api"},
				"tts-speaker": {Pacticipant: "tts-speaker", GateOnly: true},
			}},
		},
	}
}

func gateOnlyDeps(broker release.DeployBroker, compose release.ComposeDeployer) release.DeployDeps {
	deps := baseDeps(broker, compose)
	deps.Strategy = &fakeStrategy{out: map[string]versioning.Release{
		"api":         {Version: "v1"},
		"tts-speaker": {Version: "v1"},
	}}
	return deps
}

// TestDeploy_GateOnlyService_ExcludedFromComposeUp proves the core fix: a
// gate_only service is gated and recorded, but never passed to compose Up.
func TestDeploy_GateOnlyService_ExcludedFromComposeUp(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true, Reason: "ok"}, nil
		},
	}
	cp := &fakeCompose{
		ps:             []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}},
		configServices: []string{"api"}, // tts-speaker deliberately absent — it runs elsewhere
	}
	deps := gateOnlyDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), gateOnlyConfig(), "production", "", false, deps)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Empty(t, report.FailedAtStep)

	require.Len(t, cp.upCalls, 1)
	assert.Equal(t, []string{"api"}, cp.upCalls[0].Services, "gate_only service must not reach compose up")

	// Both services were still gated and recorded (fakeDeployBroker counts
	// CanIDeploy and RecordDeployment calls together: 2 gate + 2 record).
	assert.Equal(t, 4, bk.apiCalls, "gate_only service must still be gated")
	require.Len(t, bk.recordCalls, 2, "gate_only service must still be recorded")
	pacticipants := []string{bk.recordCalls[0].Pacticipant, bk.recordCalls[1].Pacticipant}
	assert.ElementsMatch(t, []string{"api", "tts-speaker"}, pacticipants)
}

// TestDeploy_AllGateOnly_SkipsComposeUp proves that when every mapped
// service in scope is gate_only, compose Up/Pull are skipped entirely
// rather than called with an empty service list (which docker compose
// would interpret as "every service in the project").
func TestDeploy_AllGateOnly_SkipsComposeUp(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{configServices: []string{}}
	deps := gateOnlyDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), gateOnlyConfig(), "production", "tts-speaker", false, deps)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Empty(t, cp.upCalls, "compose up must never be called with zero services")
	assert.Empty(t, cp.pullCalls)
	require.Len(t, bk.recordCalls, 1)
	assert.Equal(t, "tts-speaker", bk.recordCalls[0].Pacticipant)
}

// TestDeploy_ComposeCoverage_MissingService_FailsBeforeGate proves plan-time
// validation runs before the Pact gate: a mapped, non-gate_only service
// missing from Compose fails the deploy without ever calling can-i-deploy.
func TestDeploy_ComposeCoverage_MissingService_FailsBeforeGate(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{configServices: []string{}} // "api" missing entirely
	deps := gateOnlyDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), gateOnlyConfig(), "production", "", false, deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"api"`)
	assert.Contains(t, err.Error(), "gate_only: true")
	assert.Empty(t, report.FailedAtStep, "plan/config errors are not attributed to a pipeline step")
	assert.Equal(t, 0, bk.apiCalls, "the gate must never run when compose coverage validation fails")
	assert.Empty(t, cp.upCalls)
}

// TestDeploy_ComposeCoverage_MisconfiguredGateOnly_WarnsButProceeds proves a
// gate_only service that DOES exist in compose only warns; it does not
// block the deploy, and it is still excluded from compose up.
func TestDeploy_ComposeCoverage_MisconfiguredGateOnly_WarnsButProceeds(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{
		ps:             []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}},
		configServices: []string{"api", "tts-speaker"},
	}
	deps := gateOnlyDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()
	ui := &silentUI{}
	deps.UI = ui

	report, err := release.Deploy(context.Background(), gateOnlyConfig(), "production", "", false, deps)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.True(t, eventsContain(ui.events, "warn:") && eventsContain(ui.events, "tts-speaker"),
		"expected a misconfiguration warning, got %v", ui.events)
	require.Len(t, cp.upCalls, 1)
	assert.Equal(t, []string{"api"}, cp.upCalls[0].Services, "gate_only stays excluded from compose up even if it exists in compose")
}

// TestDeploy_ComposeCoverage_ConfigServicesError_NonDryRun_Fails proves no
// silent fallback: a failure to resolve the compose service list is a hard
// error for a real (non-dry-run) deploy.
func TestDeploy_ComposeCoverage_ConfigServicesError_NonDryRun_Fails(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{configServicesErr: errors.New("docker: not found")}
	deps := gateOnlyDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	_, err := release.Deploy(context.Background(), gateOnlyConfig(), "production", "", false, deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker: not found")
	assert.Equal(t, 0, bk.apiCalls)
}

// TestDeploy_ComposeCoverage_ConfigServicesError_DryRun_SkipsValidation
// proves --dry-run tolerates an unreachable compose for planning purposes:
// the gate still runs (dry-run's whole point), but coverage validation is
// skipped with a loud warning rather than failing the plan.
func TestDeploy_ComposeCoverage_ConfigServicesError_DryRun_SkipsValidation(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{configServicesErr: errors.New("docker: not found")}
	deps := gateOnlyDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()
	ui := &silentUI{}
	deps.UI = ui

	report, err := release.Deploy(context.Background(), gateOnlyConfig(), "production", "", true, deps)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 2, bk.apiCalls, "dry-run gate must still run even when coverage validation is skipped")
	assert.True(t, eventsContain(ui.events, "warn:"), "expected a warning that coverage validation was skipped")
}

// --- silent rollback degradation ------------------------------------------
// See the fix for: pre-deploy image capture failing left the snapshot with
// empty Images and a later auto-rollback silently skipped, with nothing but
// a slog line to explain why. Both the failure and the resulting skip must
// now be loud in Progress/UI output and recorded on the RollbackReport.

// TestDeploy_PreSnapshotImageCaptureFails_WarnsLoudlyAndSkipsRollbackLoudly
// proves both halves of the fix: (1) CaptureSnapshot's failure is surfaced
// via UI.Warn at snapshot time, and (2) when compose-up then fails and
// auto-rollback (default on) tries to run, the skip is itself loud — both
// via UI.Warn and via explicit fields on the persisted RollbackReport.
func TestDeploy_PreSnapshotImageCaptureFails_WarnsLoudlyAndSkipsRollbackLoudly(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	cp := &fakeCompose{
		upErr:     errors.New("compose boom"),
		renderErr: errors.New("docker: config render failed"),
	}
	ui := &silentUI{}
	deps := baseDeps(bk, cp)
	deps.UI = ui
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.Error(t, err)
	require.NotNil(t, report)
	require.NotNil(t, report.Pre)
	assert.True(t, report.Pre.ImageCaptureFailed)
	assert.Contains(t, report.Pre.ImageCaptureFailReason, "docker: config render failed")

	// (1) Loud warning at snapshot time.
	assert.True(t, eventsContain(ui.events, "rollback will not be possible"),
		"expected a loud pre-snapshot warning, got %v", ui.events)

	// (2) Auto-rollback (default on) must be skipped, loudly, with the
	// RollbackReport itself flagging the root cause — not just SkipReason
	// free text.
	require.NotNil(t, report.Rollback)
	assert.True(t, report.Rollback.Skipped)
	assert.True(t, report.Rollback.ImageCaptureFailed)
	assert.Contains(t, report.Rollback.ImageCaptureFailReason, "docker: config render failed")
	assert.True(t, eventsContain(ui.events, "rollback is not possible for this deploy"),
		"expected a loud rollback-skip warning, got %v", ui.events)
}

// TestDeploy_RollbackSkipped_NoImageCaptureFailure_StillWarnsButNoFlag
// proves the benign skip path (e.g. images already match, or no pre
// snapshot at all) still warns via UI (loud, not slog-only) but does NOT
// set RollbackReport.ImageCaptureFailed — that flag is reserved for the
// "we genuinely could not capture rollback state" case.
func TestDeploy_RollbackSkipped_NoImageCaptureFailure_StillWarnsButNoFlag(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
	}
	// No renderCfg/renderErr set on fakeCompose → RenderConfigJSON returns
	// an empty (but successful) RenderedConfig, so pre.Images is empty
	// without any capture failure — a "fresh deploy" style skip.
	cp := &fakeCompose{upErr: errors.New("compose boom")}
	ui := &silentUI{}
	deps := baseDeps(bk, cp)
	deps.UI = ui
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), baseConfig(), "production", "", false, deps)
	require.Error(t, err)
	require.NotNil(t, report)
	require.NotNil(t, report.Pre)
	assert.False(t, report.Pre.ImageCaptureFailed)

	require.NotNil(t, report.Rollback)
	assert.True(t, report.Rollback.Skipped)
	assert.False(t, report.Rollback.ImageCaptureFailed, "a benign empty-images skip must not be flagged as a capture failure")
	assert.True(t, eventsContain(ui.events, "skipped"), "even a benign skip must be loud, not slog-only")
}

// --- record-deployment partial failure -----------------------------------

func twoServiceConfig() *config.Config {
	return &config.Config{
		Broker: config.BrokerConfig{BaseURL: "http://broker"},
		Environments: map[string]config.Environment{
			"production": {Services: map[string]config.ServiceMapping{
				"api": {Pacticipant: "api"},
				"web": {Pacticipant: "web"},
			}},
		},
	}
}

func twoServiceDeps(broker release.DeployBroker, compose release.ComposeDeployer) release.DeployDeps {
	deps := baseDeps(broker, compose)
	deps.Strategy = &fakeStrategy{out: map[string]versioning.Release{
		"api": {Version: "v1"},
		"web": {Version: "v2"},
	}}
	return deps
}

// TestDeploy_RecordDeployment_PartialFailure_AttemptsAllAndItemizes proves
// the fix for the "stops at first error" defect: every service's
// record-deployment call is attempted even after an earlier one fails, and
// the returned error itemizes exactly which services were recorded and
// which were not.
func TestDeploy_RecordDeployment_PartialFailure_AttemptsAllAndItemizes(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
		recordDeploymentFunc: func(_ context.Context, in broker.RecordDeploymentInput) error {
			if in.Pacticipant == "web" {
				return errors.New("broker 503")
			}
			return nil
		},
	}
	cp := &fakeCompose{
		ps:             []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}, {Service: "web", State: "running", Health: "healthy"}},
		configServices: []string{"api", "web"},
	}
	deps := twoServiceDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), twoServiceConfig(), "production", "", false, deps)
	require.Error(t, err)
	require.NotNil(t, report)
	assert.Equal(t, release.StepRecord, report.FailedAtStep)

	// Both services must have been attempted, in order, regardless of the
	// first one's outcome.
	require.Len(t, bk.recordCalls, 2, "record-deployment MUST be attempted for every service, not just until the first failure")
	pacticipants := []string{bk.recordCalls[0].Pacticipant, bk.recordCalls[1].Pacticipant}
	assert.ElementsMatch(t, []string{"api", "web"}, pacticipants)

	// The report itemizes the split.
	require.Len(t, report.RecordResults, 2)
	byService := map[string]release.RecordResult{}
	for _, r := range report.RecordResults {
		byService[r.Service] = r
	}
	assert.True(t, byService["api"].Recorded, "api should have been recorded successfully")
	assert.False(t, byService["web"].Recorded, "web should be marked as not recorded")
	assert.Error(t, byService["web"].Err)

	// The returned error itemizes recorded vs. failed services.
	var recErr *release.RecordDeploymentError
	require.ErrorAs(t, err, &recErr)
	assert.ElementsMatch(t, []string{"api"}, recErr.Recorded())
	assert.ElementsMatch(t, []string{"web"}, recErr.Unrecorded())
	assert.Contains(t, err.Error(), "api")
	assert.Contains(t, err.Error(), "web")
}

// TestDeploy_RecordDeployment_AllFail_ItemizesAllAsUnrecorded is the
// degenerate case: every service fails, so Recorded() is empty and
// Unrecorded() lists everything.
func TestDeploy_RecordDeployment_AllFail_ItemizesAllAsUnrecorded(t *testing.T) {
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
		recordDeploymentFunc: func(_ context.Context, _ broker.RecordDeploymentInput) error {
			return errors.New("broker unreachable")
		},
	}
	cp := &fakeCompose{
		ps:             []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}, {Service: "web", State: "running", Health: "healthy"}},
		configServices: []string{"api", "web"},
	}
	deps := twoServiceDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(context.Background(), twoServiceConfig(), "production", "", false, deps)
	require.Error(t, err)
	require.Len(t, bk.recordCalls, 2, "both services must still be attempted")

	var recErr *release.RecordDeploymentError
	require.ErrorAs(t, err, &recErr)
	assert.Empty(t, recErr.Recorded())
	assert.ElementsMatch(t, []string{"api", "web"}, recErr.Unrecorded())
	assert.Len(t, report.RecordResults, 2)
}

// TestDeploy_RecordDeployment_CtxCancelledMidLoop_StopsIssuingCalls proves
// the M2 fix: once the deploy's context is cancelled partway through
// recordAllDeployments, the remaining services must NOT be POSTed to the
// broker at all — they are marked "not attempted", not "attempted and
// failed". Before the fix, recordAllDeployments kept looping through every
// remaining service, issuing HTTP calls doomed to fail against a cancelled
// context.
func TestDeploy_RecordDeployment_CtxCancelledMidLoop_StopsIssuingCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bk := &fakeDeployBroker{
		canIDeployFunc: func(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
			return &broker.CanIDeployResult{Deployable: true}, nil
		},
		recordDeploymentFunc: func(_ context.Context, _ broker.RecordDeploymentInput) error {
			// Cancel after the first service's call lands, regardless of
			// which service that is (plan.Services is sorted, so it's
			// deterministically "api" first).
			cancel()
			return nil
		},
	}
	cp := &fakeCompose{
		ps:             []composeadapter.ContainerStatus{{Service: "api", State: "running", Health: "healthy"}, {Service: "web", State: "running", Health: "healthy"}},
		configServices: []string{"api", "web"},
	}
	deps := twoServiceDeps(bk, cp)
	deps.SnapshotDir = t.TempDir()

	report, err := release.Deploy(ctx, twoServiceConfig(), "production", "", false, deps)
	require.Error(t, err)
	require.NotNil(t, report)
	assert.Equal(t, release.StepRecord, report.FailedAtStep)

	require.Len(t, bk.recordCalls, 1, "the broker must not be called again once ctx is cancelled")
	require.Len(t, report.RecordResults, 2)

	first, second := report.RecordResults[0], report.RecordResults[1]
	assert.True(t, first.Recorded, "the service recorded before cancellation must still show as recorded")
	assert.False(t, second.Recorded)
	assert.ErrorIs(t, second.Err, release.ErrRecordNotAttempted,
		"the never-called service must be distinguishable from an attempted-and-failed one")

	var recErr *release.RecordDeploymentError
	require.ErrorAs(t, err, &recErr)
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
