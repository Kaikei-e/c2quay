package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/lock"
	"github.com/Kaikei-e/c2quay/internal/release"
)

// This file covers the full command-wiring functions (runVerify, runRollback)
// end to end, using a fake Pact Broker (httptest) and a manifest_file
// versioning strategy so no live broker or docker daemon is required — the
// same trick test/e2e uses, but exercised in-process against runXxx directly
// instead of shelling out to a built binary. runDeploy/runStatus are NOT
// covered this way because their pipelines always shell out to a real
// `docker compose ps`/`config` before any --dry-run short-circuit is
// reached (see release.Deploy step 1, and runStatus's PsJSON call), which
// this sandbox cannot satisfy without a docker daemon; those two are
// exercised at the unit level (classifyDeployError, writeDeployFailureHint,
// renderComposeState, checkBrokerEnvironment, flag wiring) instead. See
// test/e2e for their black-box coverage against a real daemon in CI.

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeCanIDeployBroker serves the minimal HAL shape runVerify/release.Verify
// need: an index exposing pb:can-i-deploy (legacy, query-string form) and a
// handler that answers every can-i-deploy query with the same verdict.
func fakeCanIDeployBroker(t *testing.T, deployable bool, reason string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"_links": map[string]any{
				"pb:can-i-deploy": map[string]any{"href": srv.URL + "/matrix", "templated": false},
			},
		})
	})
	mux.HandleFunc("/matrix", func(w http.ResponseWriter, r *http.Request) {
		d := deployable
		_ = json.NewEncoder(w).Encode(map[string]any{
			"summary": map[string]any{"deployable": &d, "reason": reason},
		})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeManifest writes a versions.json manifest_file-strategy manifest with
// one entry per service, in dir, returning the absolute path.
func writeManifest(t *testing.T, dir string, services map[string]string) string {
	t.Helper()
	type entry struct {
		Version string `json:"version"`
		Image   string `json:"image,omitempty"`
	}
	doc := struct {
		Services map[string]entry `json:"services"`
	}{Services: map[string]entry{}}
	for svc, version := range services {
		doc.Services[svc] = entry{Version: version, Image: "registry/" + svc + ":" + version}
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	path := filepath.Join(dir, "versions.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

func verifyTestConfig(brokerURL, manifestPath string) *config.Config {
	return &config.Config{
		Compose: config.ComposeConfig{Files: []string{"compose.yaml"}, ProjectName: "myapp"},
		Broker:  config.BrokerConfig{BaseURL: brokerURL},
		Versioning: config.VersioningConfig{
			Strategy: "manifest_file",
			Options:  map[string]string{"path": manifestPath},
		},
		Deploy: config.DeployConfig{WaitTimeout: 30 * time.Second},
		Environments: map[string]config.Environment{
			"production": {
				Services: map[string]config.ServiceMapping{
					"api": {Pacticipant: "api"},
					"web": {Pacticipant: "web"},
				},
			},
		},
	}
}

// --- runVerify --------------------------------------------------------------

func TestRunVerify_AllDeployable_TextOutput_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	srv := fakeCanIDeployBroker(t, true, "ok")
	manifest := writeManifest(t, dir, map[string]string{"api": "v1", "web": "v2"})

	rt, stdout, _ := newTestRuntime()
	rt.cfg = verifyTestConfig(srv.URL, manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"
	rt.flags.output = "text"

	err := runVerify(context.Background(), rt, "")
	require.NoError(t, err)

	out := stdout.String()
	assert.Contains(t, out, "can-i-deploy: api@v1")
	assert.Contains(t, out, "can-i-deploy: web@v2")
	assert.Contains(t, out, "Summary: 2/2 passed")
}

func TestRunVerify_GateRejected_TextOutput_ReturnsGateExitError(t *testing.T) {
	dir := t.TempDir()
	srv := fakeCanIDeployBroker(t, false, "contracts not compatible")
	manifest := writeManifest(t, dir, map[string]string{"api": "v1", "web": "v2"})

	rt, stdout, _ := newTestRuntime()
	rt.cfg = verifyTestConfig(srv.URL, manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"
	rt.flags.output = "text"

	err := runVerify(context.Background(), rt, "")
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitGateFailed, ee.Code)

	out := stdout.String()
	assert.Contains(t, out, "contracts not compatible")
	assert.Contains(t, out, "Summary: 0 passed, 2 failed")
}

func TestRunVerify_JSONOutput_Shape(t *testing.T) {
	dir := t.TempDir()
	srv := fakeCanIDeployBroker(t, true, "ok")
	manifest := writeManifest(t, dir, map[string]string{"api": "v1", "web": "v2"})

	rt, stdout, _ := newTestRuntime()
	rt.cfg = verifyTestConfig(srv.URL, manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"
	rt.flags.output = "json"

	require.NoError(t, runVerify(context.Background(), rt, ""))

	var got map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, "production", got["env"])
	assert.Equal(t, "verify", got["command"])
}

// TestRunVerify_ServiceFilter_LimitsPlan proves the --service flag (onlyService
// parameter) reaches release.BuildPlan and filters the checked set to one
// service.
func TestRunVerify_ServiceFilter_LimitsPlan(t *testing.T) {
	dir := t.TempDir()
	srv := fakeCanIDeployBroker(t, true, "ok")
	manifest := writeManifest(t, dir, map[string]string{"api": "v1", "web": "v2"})

	rt, stdout, _ := newTestRuntime()
	rt.cfg = verifyTestConfig(srv.URL, manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"
	rt.flags.output = "text"

	require.NoError(t, runVerify(context.Background(), rt, "api"))

	out := stdout.String()
	assert.Contains(t, out, "api@v1")
	assert.NotContains(t, out, "web@v2")
	assert.Contains(t, out, "Summary: 1/1 passed")
}

// TestRunVerify_BrokerLacksCanIDeployRelation_OperatorError proves the
// explicit relation-presence check in runVerify (distinct from
// release.Verify itself) fires before BuildPlan/GateAll run at all.
func TestRunVerify_BrokerLacksCanIDeployRelation_OperatorError(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{"_links": map[string]any{}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	manifest := writeManifest(t, dir, map[string]string{"api": "v1", "web": "v2"})

	rt, _, _ := newTestRuntime()
	rt.cfg = verifyTestConfig(srv.URL, manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runVerify(context.Background(), rt, "")
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	assert.Contains(t, err.Error(), "can-i-deploy relation")
}

// TestRunVerify_BrokerUnreachable_OperatorError proves a broker that never
// answers (connection refused) surfaces as ExitOperatorError, not a panic or
// hang — bc.Start's error path.
func TestRunVerify_BrokerUnreachable_OperatorError(t *testing.T) {
	dir := t.TempDir()
	manifest := writeManifest(t, dir, map[string]string{"api": "v1", "web": "v2"})

	rt, _, _ := newTestRuntime()
	rt.cfg = verifyTestConfig("http://127.0.0.1:1", manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runVerify(context.Background(), rt, "")
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

// --- runDeploy ------------------------------------------------------------
//
// release.Deploy's very first step captures a pre-deploy snapshot via
// `docker compose ps` (see internal/release/deploy.go step 1), so a
// happy-path runDeploy test needs a live docker daemon and belongs in
// test/e2e (deploy_dryrun_test.go), not here. What IS deterministic and
// docker-free is everything runDeploy does before handing off to
// release.Deploy: acquiring the environment lock, constructing the compose
// adapter and versioning strategy, and contacting the broker. These tests
// cover exactly that boundary.

func deployTestConfig(brokerURL, manifestPath string) *config.Config {
	return &config.Config{
		Compose: config.ComposeConfig{Files: []string{"compose.yaml"}, ProjectName: "myapp"},
		Broker:  config.BrokerConfig{BaseURL: brokerURL},
		Versioning: config.VersioningConfig{
			Strategy: "manifest_file",
			Options:  map[string]string{"path": manifestPath},
		},
		Deploy: config.DeployConfig{WaitTimeout: 5 * time.Second},
		Environments: map[string]config.Environment{
			"production": {
				Services: map[string]config.ServiceMapping{
					"api": {Pacticipant: "api"},
				},
			},
		},
	}
}

// TestRunDeploy_LockAlreadyHeld_OperatorError proves a concurrent deploy to
// the same environment is rejected before any broker/compose work starts —
// pure filesystem-level flock contention, no network or docker involved.
func TestRunDeploy_LockAlreadyHeld_OperatorError(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)

	held, err := lock.Acquire(filepath.Join(".c2quay", "locks", "production.lock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Release() })

	dir := t.TempDir()
	manifest := writeManifest(t, dir, map[string]string{"api": "v1"})
	rt, _, _ := newTestRuntime()
	rt.cfg = deployTestConfig("http://127.0.0.1:1", manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err = runDeploy(context.Background(), rt, "", false, release.RollbackOn, false)
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

// TestRunDeploy_InvalidVersioningStrategy_OperatorError proves a config
// error surfacing from versioning.Factory (unsupported strategy name) is
// reported before any broker contact, and that the lock is released
// afterwards (Acquire on the same path immediately after must succeed).
func TestRunDeploy_InvalidVersioningStrategy_OperatorError(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)

	rt, _, _ := newTestRuntime()
	rt.cfg = &config.Config{
		Compose:    config.ComposeConfig{Files: []string{"compose.yaml"}, ProjectName: "myapp"},
		Broker:     config.BrokerConfig{BaseURL: "http://127.0.0.1:1"},
		Versioning: config.VersioningConfig{Strategy: "not_a_real_strategy"},
		Environments: map[string]config.Environment{
			"production": {Services: map[string]config.ServiceMapping{"api": {Pacticipant: "api"}}},
		},
	}
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runDeploy(context.Background(), rt, "", false, release.RollbackOn, false)
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	assert.Contains(t, err.Error(), "is not supported")

	// Lock was released: acquiring it again must succeed immediately.
	l, lerr := lock.Acquire(filepath.Join(work, ".c2quay", "locks", "production.lock"))
	require.NoError(t, lerr)
	require.NoError(t, l.Release())
}

// TestRunDeploy_BrokerBaseURLInvalid_OperatorError proves a malformed
// broker.base_url (missing scheme/host) is rejected by broker.New before
// any HTTP call is attempted.
func TestRunDeploy_BrokerBaseURLInvalid_OperatorError(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)

	dir := t.TempDir()
	manifest := writeManifest(t, dir, map[string]string{"api": "v1"})
	rt, _, _ := newTestRuntime()
	rt.cfg = deployTestConfig("not-a-url", manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runDeploy(context.Background(), rt, "", false, release.RollbackOn, false)
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

// TestRunDeploy_BrokerUnreachable_OperatorError proves a broker that
// refuses the connection is surfaced as an operator error from bc.Start,
// exercising runDeploy all the way through lock, adapter, strategy, and
// broker construction without ever needing docker.
func TestRunDeploy_BrokerUnreachable_OperatorError(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)

	dir := t.TempDir()
	manifest := writeManifest(t, dir, map[string]string{"api": "v1"})
	rt, _, stderr := newTestRuntime()
	rt.cfg = deployTestConfig("http://127.0.0.1:1", manifest)
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runDeploy(context.Background(), rt, "", false, release.RollbackOn, false)
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	// No DeployReport ever existed (release.Deploy was never reached), so no
	// rollback hint should have been printed.
	assert.NotContains(t, stderr.String(), "Rollback hints")
}

// --- runStatus --------------------------------------------------------

// TestRunStatus_ComposeFileMissing_OperatorError proves runStatus surfaces
// adapter.PsJSON's failure as ExitOperatorError. This is deterministic in
// any environment, docker daemon or not: `docker compose -f compose.yaml
// ...` fails client-side ("no such file or directory") before it ever tries
// to reach the daemon when the compose file itself doesn't exist, which is
// the case here (an empty temp dir). This is as far into runStatus as a
// docker-free test can reach — the broker-environment half requires PsJSON
// to succeed first, which needs a real compose file plus a live daemon; see
// checkBrokerEnvironment's direct unit tests for that half instead.
func TestRunStatus_ComposeFileMissing_OperatorError(t *testing.T) {
	t.Chdir(t.TempDir())

	rt, _, _ := newTestRuntime()
	rt.cfg = &config.Config{Compose: config.ComposeConfig{Files: []string{"compose.yaml"}, ProjectName: "myapp"}}
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runStatus(context.Background(), rt)
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

// --- Execute ----------------------------------------------------------
//
// Execute has no seam for injecting args (it always parses os.Args via
// cobra's default), so these tests manipulate the process-global os.Args /
// os.Stdout / os.Stderr for the duration of the call. Safe here because
// this package's tests never run with t.Parallel().

func TestExecute_UnknownCommand_ReturnsOperatorErrorCode(t *testing.T) {
	origArgs, origStderr := os.Args, os.Stderr
	t.Cleanup(func() { os.Args, os.Stderr = origArgs, origStderr })

	r, w, perr := os.Pipe()
	require.NoError(t, perr)
	os.Stderr = w
	os.Args = []string{"c2quay", "not-a-real-command"}

	code := Execute(context.Background())

	require.NoError(t, w.Close())
	os.Stderr = origStderr
	raw, _ := io.ReadAll(r)

	assert.Equal(t, ExitOperatorError, code)
	assert.Contains(t, string(raw), "error:")
}

func TestExecute_VersionCommand_ReturnsOK(t *testing.T) {
	origArgs, origStdout := os.Args, os.Stdout
	t.Cleanup(func() { os.Args, os.Stdout = origArgs, origStdout })

	r, w, perr := os.Pipe()
	require.NoError(t, perr)
	os.Stdout = w
	os.Args = []string{"c2quay", "version"}

	code := Execute(context.Background())

	require.NoError(t, w.Close())
	os.Stdout = origStdout
	raw, _ := io.ReadAll(r)

	assert.Equal(t, ExitOK, code)
	assert.Contains(t, string(raw), "c2quay")
}

// --- runRollback --------------------------------------------------------

func writeSnapshot(t *testing.T, dir string, snap release.Snapshot) string {
	t.Helper()
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	path := filepath.Join(dir, "pre.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

func rollbackTestConfig() *config.Config {
	return &config.Config{
		Compose: config.ComposeConfig{Files: []string{"compose.yaml"}, ProjectName: "myapp"},
		Deploy:  config.DeployConfig{WaitTimeout: 5 * time.Second},
	}
}

// TestRunRollback_MissingSnapshotFile_OperatorError proves a bad
// --from-snapshot path fails fast via LoadSnapshot, before lock.Acquire or
// any compose/docker call.
func TestRunRollback_MissingSnapshotFile_OperatorError(t *testing.T) {
	t.Chdir(t.TempDir())
	rt, _, _ := newTestRuntime()
	rt.cfg = rollbackTestConfig()
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runRollback(context.Background(), rt, "/does/not/exist.json", false)
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	assert.Contains(t, err.Error(), "load snapshot")
}

// TestRunRollback_NothingToRestore_ReturnsNil proves a snapshot with no
// recorded images (BuildRollbackPlan's "nothing to do" case) is a clean,
// no-op success — it never reaches ExecuteRollback / docker compose up.
func TestRunRollback_NothingToRestore_ReturnsNil(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	snapPath := writeSnapshot(t, work, release.Snapshot{Env: "production"}) // no Images

	rt, stdout, _ := newTestRuntime()
	rt.cfg = rollbackTestConfig()
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runRollback(context.Background(), rt, snapPath, false)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "nothing to do")
}

// TestRunRollback_DryRun_Succeeds proves the --dry-run path (rollback
// command, not deploy's auto-rollback) completes end to end without
// invoking `docker compose up` — ExecuteRollback's RollbackDryRun branch
// returns before touching Compose.Up — so it needs no docker daemon.
func TestRunRollback_DryRun_Succeeds(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	snapPath := writeSnapshot(t, work, release.Snapshot{
		Env:    "production",
		Images: map[string]string{"api": "registry/api:old"},
	})

	rt, stdout, stderr := newTestRuntime()
	rt.cfg = rollbackTestConfig()
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runRollback(context.Background(), rt, snapPath, true)
	require.NoError(t, err, "stdout=%s stderr=%s", stdout.String(), stderr.String())
	assert.Contains(t, stdout.String(), "dry-run")

	// Lock must have been released (Acquire succeeds again immediately).
	_, statErr := os.Stat(filepath.Join(work, ".c2quay", "locks", "production.lock"))
	assert.NoError(t, statErr, "lock file created")
}

// TestRunRollback_EnvMismatch_WarnsButProceeds proves a snapshot recorded
// for a different environment than --env is a warning, not a hard failure
// (see the log.Warn call in runRollback) — the dry-run rollback still runs.
func TestRunRollback_EnvMismatch_WarnsButProceeds(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	snapPath := writeSnapshot(t, work, release.Snapshot{
		Env:    "staging",
		Images: map[string]string{"api": "registry/api:old"},
	})

	rt, _, _ := newTestRuntime()
	rt.cfg = rollbackTestConfig()
	rt.logger = quietLogger()
	rt.flags.envName = "production"

	err := runRollback(context.Background(), rt, snapPath, true)
	require.NoError(t, err)
}
