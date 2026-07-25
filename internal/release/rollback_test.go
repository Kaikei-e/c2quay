package release_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/release"
)

// --- PolicyFor ---------------------------------------------------------

func TestPolicyFor(t *testing.T) {
	cases := []struct {
		step release.FailedStep
		want bool
	}{
		{release.StepGate, false},
		{release.StepComposeUp, true},
		{release.StepSmoke, true},
		{release.StepRecord, false},
		{release.FailedStep(""), false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, release.PolicyFor(c.step), "step=%s", c.step)
	}
}

// --- ResolveRollbackMode / ParseRollbackMode ---------------------------

func TestResolveRollbackMode(t *testing.T) {
	// Zero value resolves to RollbackOn per the opt-out default.
	assert.Equal(t, release.RollbackOn, release.ResolveRollbackMode(""))
	assert.Equal(t, release.RollbackOn, release.ResolveRollbackMode(release.RollbackOn))
	assert.Equal(t, release.RollbackOff, release.ResolveRollbackMode(release.RollbackOff))
	assert.Equal(t, release.RollbackDryRun, release.ResolveRollbackMode(release.RollbackDryRun))
	// Unknown values fall back to Off — safer than running a surprise rollback.
	assert.Equal(t, release.RollbackOff, release.ResolveRollbackMode(release.RollbackMode("bogus")))
}

func TestParseRollbackMode(t *testing.T) {
	cases := []struct {
		in      string
		want    release.RollbackMode
		wantErr bool
	}{
		{"on", release.RollbackOn, false},
		{"ON", release.RollbackOn, false},
		{"auto", release.RollbackOn, false},
		{"", release.RollbackOn, false},
		{"off", release.RollbackOff, false},
		{"disabled", release.RollbackOff, false},
		{"dry-run", release.RollbackDryRun, false},
		{"dryrun", release.RollbackDryRun, false},
		{"yolo", "", true},
	}
	for _, c := range cases {
		got, err := release.ParseRollbackMode(c.in)
		if c.wantErr {
			require.Error(t, err, "in=%q", c.in)
			continue
		}
		require.NoError(t, err, "in=%q", c.in)
		assert.Equal(t, c.want, got, "in=%q", c.in)
	}
}

// --- BuildRollbackPlan -------------------------------------------------

func TestBuildRollbackPlan(t *testing.T) {
	t.Run("images differ → plan with only changed services", func(t *testing.T) {
		pre := &release.Snapshot{
			Env:    "production",
			Images: map[string]string{"api": "api:v1", "web": "web:v1"},
		}
		current := map[string]string{"api": "api:v2", "web": "web:v1"}
		plan, ok, reason := release.BuildRollbackPlan(pre, current)
		require.True(t, ok, "reason=%q", reason)
		assert.Equal(t, []string{"api"}, plan.Services)
		assert.Equal(t, "api:v1", plan.Images["api"])
		assert.Equal(t, "production", plan.Env)
	})

	t.Run("current nil → restore every service in snapshot", func(t *testing.T) {
		pre := &release.Snapshot{Images: map[string]string{"api": "api:v1", "web": "web:v1"}}
		plan, ok, _ := release.BuildRollbackPlan(pre, nil)
		require.True(t, ok)
		assert.ElementsMatch(t, []string{"api", "web"}, plan.Services)
	})

	t.Run("images match → skip", func(t *testing.T) {
		pre := &release.Snapshot{Images: map[string]string{"api": "api:v1"}}
		cur := map[string]string{"api": "api:v1"}
		_, ok, reason := release.BuildRollbackPlan(pre, cur)
		assert.False(t, ok)
		assert.Contains(t, reason, "already match")
	})

	t.Run("empty pre images → skip", func(t *testing.T) {
		pre := &release.Snapshot{}
		_, ok, reason := release.BuildRollbackPlan(pre, map[string]string{"api": "api:v1"})
		assert.False(t, ok)
		assert.Contains(t, reason, "no recorded images")
	})

	t.Run("nil pre → skip", func(t *testing.T) {
		_, ok, reason := release.BuildRollbackPlan(nil, nil)
		assert.False(t, ok)
		assert.NotEmpty(t, reason)
	})

	// TestBuildRollbackPlan (gate_only case): a gate_only service is never
	// captured in a snapshot's Images map in the first place — Images comes
	// from `docker compose config`'s rendered services (see CaptureSnapshot
	// / RenderConfigJSON.ImagesByService), and gate_only services are by
	// definition absent from Compose. BuildRollbackPlan therefore needs no
	// gate_only-specific logic: it naturally never proposes rolling back a
	// service that was never running under Compose to begin with. This
	// pins that invariant down as an explicit regression test. See
	// ADR 0013.
	t.Run("gate_only service absent from pre snapshot is never proposed for rollback", func(t *testing.T) {
		pre := &release.Snapshot{
			Env: "production",
			// "tts-speaker" is mapped in c2quay.yml but gate_only: true, so
			// it was never in the compose config and never made it into
			// Images — exactly like "api" here, minus the entry.
			Images: map[string]string{"api": "api:v1"},
		}
		current := map[string]string{"api": "api:v2"}
		plan, ok, reason := release.BuildRollbackPlan(pre, current)
		require.True(t, ok, "reason=%q", reason)
		assert.Equal(t, []string{"api"}, plan.Services, "gate_only service must not appear in the rollback plan")
		assert.NotContains(t, plan.Images, "tts-speaker")
	})

	t.Run("blank previous image skipped", func(t *testing.T) {
		pre := &release.Snapshot{Images: map[string]string{"api": "", "web": "web:v1"}}
		plan, ok, _ := release.BuildRollbackPlan(pre, nil)
		require.True(t, ok)
		assert.Equal(t, []string{"web"}, plan.Services)
	})
}

// --- WriteOverrideYAML --------------------------------------------------

func TestWriteOverrideYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "override.yml")

	require.NoError(t, release.WriteOverrideYAML(path, map[string]string{
		"api": "api:v1",
		"web": "registry.example/web@sha256:abcd",
	}))
	raw, err := os.ReadFile(path) //nolint:gosec
	require.NoError(t, err)
	content := string(raw)

	assert.Contains(t, content, "services:")
	assert.Contains(t, content, "api:")
	assert.Contains(t, content, "image: api:v1")
	assert.Contains(t, content, "web:")
	// `@` triggers quoted emission defensively.
	assert.Contains(t, content, `"registry.example/web@sha256:abcd"`)

	// Services appear in sorted order for deterministic output.
	apiIdx := strings.Index(content, "  api:")
	webIdx := strings.Index(content, "  web:")
	assert.Less(t, apiIdx, webIdx)
}

func TestWriteOverrideYAML_Empty(t *testing.T) {
	err := release.WriteOverrideYAML(filepath.Join(t.TempDir(), "x.yml"), map[string]string{})
	require.Error(t, err)
}

// --- ExecuteRollback ---------------------------------------------------

type rbFakeCompose struct {
	upErr     error
	upCalls   []composeadapter.UpOptions
	ps        []composeadapter.ContainerStatus
	renderCfg *composeadapter.RenderedConfig
	renderErr error
}

func (f *rbFakeCompose) Pull(context.Context, []string, io.Writer) error { return nil }
func (f *rbFakeCompose) Up(_ context.Context, opts composeadapter.UpOptions, _ io.Writer) error {
	f.upCalls = append(f.upCalls, opts)
	return f.upErr
}
func (f *rbFakeCompose) PsJSON(context.Context) ([]composeadapter.ContainerStatus, error) {
	return f.ps, nil
}
func (f *rbFakeCompose) RenderConfigJSON(context.Context) (*composeadapter.RenderedConfig, error) {
	if f.renderErr != nil {
		return nil, f.renderErr
	}
	if f.renderCfg != nil {
		return f.renderCfg, nil
	}
	return &composeadapter.RenderedConfig{Services: map[string]composeadapter.RenderedService{}}, nil
}

// ConfigServices is not exercised by rollback flows (plan-time coverage
// validation is a deploy-only step); it satisfies release.ComposeDeployer.
func (f *rbFakeCompose) ConfigServices(context.Context) ([]string, error) { return nil, nil }

type rbSilentUI struct{ events []string }

func (u *rbSilentUI) Step(l, d string) { u.events = append(u.events, "step:"+l+"|"+d) }
func (u *rbSilentUI) Ok(l, d string)   { u.events = append(u.events, "ok:"+l+"|"+d) }
func (u *rbSilentUI) Fail(l, d string) { u.events = append(u.events, "fail:"+l+"|"+d) }
func (u *rbSilentUI) Warn(l, d string) { u.events = append(u.events, "warn:"+l+"|"+d) }

func baseRollbackDeps(t *testing.T, cp release.ComposeDeployer) release.RollbackDeps {
	t.Helper()
	return release.RollbackDeps{
		Compose:     cp,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		UI:          &rbSilentUI{},
		Progress:    io.Discard,
		SnapshotDir: filepath.Join(t.TempDir(), "snapshots"),
	}
}

func TestExecuteRollback_HappyPath(t *testing.T) {
	cp := &rbFakeCompose{}
	deps := baseRollbackDeps(t, cp)
	require.NoError(t, os.MkdirAll(deps.SnapshotDir, 0o750))

	plan := &release.RollbackPlan{
		Env:      "production",
		Images:   map[string]string{"api": "api:v1"},
		Services: []string{"api"},
	}
	report, err := release.ExecuteRollback(context.Background(), deps, plan, release.RollbackOn)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.True(t, report.Succeeded)
	assert.True(t, report.Attempted)
	assert.Equal(t, release.RollbackOn, report.Mode)
	require.Len(t, cp.upCalls, 1)

	// The override file lands in <snapshotDir>/../rollback/
	require.NotEmpty(t, report.OverrideFile)
	_, err = os.Stat(report.OverrideFile)
	require.NoError(t, err)
	// compose Up call passed override via ExtraFiles.
	assert.Equal(t, []string{report.OverrideFile}, cp.upCalls[0].ExtraFiles)
	assert.True(t, cp.upCalls[0].Wait)
	// Report JSON is persisted.
	require.NotEmpty(t, report.ReportFile)
	raw, rerr := os.ReadFile(report.ReportFile) //nolint:gosec
	require.NoError(t, rerr)
	var decoded release.RollbackReport
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.True(t, decoded.Succeeded)
}

func TestExecuteRollback_DryRun(t *testing.T) {
	cp := &rbFakeCompose{}
	deps := baseRollbackDeps(t, cp)
	require.NoError(t, os.MkdirAll(deps.SnapshotDir, 0o750))

	plan := &release.RollbackPlan{
		Env:      "production",
		Images:   map[string]string{"api": "api:v1"},
		Services: []string{"api"},
	}
	report, err := release.ExecuteRollback(context.Background(), deps, plan, release.RollbackDryRun)
	require.NoError(t, err)
	assert.True(t, report.Succeeded)
	assert.Empty(t, cp.upCalls, "dry-run must not invoke compose up")
	assert.Empty(t, report.OverrideFile, "dry-run must not write override")
}

func TestExecuteRollback_UpFailureSurfacesError(t *testing.T) {
	cp := &rbFakeCompose{upErr: errors.New("kaboom")}
	deps := baseRollbackDeps(t, cp)
	require.NoError(t, os.MkdirAll(deps.SnapshotDir, 0o750))

	plan := &release.RollbackPlan{
		Env:      "production",
		Images:   map[string]string{"api": "api:v1"},
		Services: []string{"api"},
	}
	report, err := release.ExecuteRollback(context.Background(), deps, plan, release.RollbackOn)
	require.Error(t, err)
	require.NotNil(t, report)
	assert.True(t, report.Attempted)
	assert.False(t, report.Succeeded)
	assert.Contains(t, report.Err, "kaboom")
	assert.NotEmpty(t, report.OverrideFile, "override still written before compose up attempt")
}

func TestExecuteRollback_NilPlanErrors(t *testing.T) {
	_, err := release.ExecuteRollback(context.Background(), baseRollbackDeps(t, &rbFakeCompose{}), nil, release.RollbackOn)
	require.Error(t, err)
}

// --- LoadSnapshot ------------------------------------------------------

func TestLoadSnapshot_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	snap := &release.Snapshot{
		Env:    "production",
		Images: map[string]string{"api": "api:v1"},
	}
	path, err := snap.Write(dir, "pre")
	require.NoError(t, err)

	loaded, err := release.LoadSnapshot(path)
	require.NoError(t, err)
	assert.Equal(t, "production", loaded.Env)
	assert.Equal(t, "api:v1", loaded.Images["api"])
}

func TestLoadSnapshot_MissingFile(t *testing.T) {
	_, err := release.LoadSnapshot(filepath.Join(t.TempDir(), "does-not-exist.json"))
	require.Error(t, err)
}
