//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireDockerDaemon skips the test when the Docker daemon is not
// reachable. Several existing e2e cases (deploy_dryrun_test.go) implicitly
// assume a live daemon and simply fail without one; this helper makes that
// dependency explicit for tests added alongside ADR 0013 so a sandboxed run
// without dockerd skips cleanly instead of failing for an unrelated reason.
func requireDockerDaemon(t *testing.T) {
	t.Helper()
	if err := exec.CommandContext(context.Background(), "docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable; skipping (see requireDockerDaemon)")
	}
}

// namedFixtureDir mirrors fixtureDir(t) (verify_test.go) but for a fixture
// other than "minimal".
func namedFixtureDir(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "fixtures", name)
}

func copyFixture(t *testing.T, src string) string {
	t.Helper()
	workDir := t.TempDir()
	for _, f := range []string{"c2quay.yml", "compose.yaml", "versions.json"} {
		raw, err := os.ReadFile(filepath.Join(src, f))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(workDir, f), raw, 0o644))
	}
	return workDir
}

// TestE2E_Deploy_GateOnly_MissingService_FailsBeforeGate proves the ADR
// 0013 fail-fast path end to end: a mapped, non-gate_only service ("ghost")
// absent from compose.yaml aborts the deploy with a clear message before
// the Pact gate ever runs. This is the regression test for the original
// incident (c2quay previously let this reach `docker compose up`, which
// hard-failed the whole batch with "no such service"). Unlike most e2e
// deploy cases, this does not require a reachable Docker daemon: plan-time
// coverage validation runs before the pre-deploy snapshot (which is the
// first step that needs the daemon), using only
// `docker compose config --services`.
func TestE2E_Deploy_GateOnly_MissingService_FailsBeforeGate(t *testing.T) {
	bin := buildBinary(t)
	srv := fakeBroker(t, true, "ok")
	workDir := copyFixture(t, namedFixtureDir(t, "gate_only_missing"))

	code, stdout, stderr := runC2Q(t, bin, workDir, srv.URL, "deploy", "--env", "production", "--dry-run")
	combined := stdout + stderr

	assert.Equal(t, 2, code, "missing non-gate_only service must exit as an operator error, got stdout=%s stderr=%s", stdout, stderr)
	assert.Contains(t, combined, `"ghost"`)
	assert.Contains(t, combined, "gate_only: true")
	assert.NotContains(t, combined, "can-i-deploy", "the gate must never run when compose coverage validation fails")
}

// TestE2E_Deploy_GateOnly_ExcludedFromComposeUp exercises the success path:
// a gate_only service ("tts-speaker") absent from compose.yaml does NOT
// trip plan-time validation, and is still gated (can-i-deploy runs for it
// too). This requires a reachable Docker daemon (the pipeline captures a
// pre-deploy snapshot via `docker compose ps` before --dry-run stops it),
// so it is skipped when the daemon is unreachable rather than failing for
// an unrelated, environment-specific reason.
func TestE2E_Deploy_GateOnly_ExcludedFromComposeUp(t *testing.T) {
	requireDockerDaemon(t)

	bin := buildBinary(t)
	srv := fakeBroker(t, true, "ok")
	workDir := copyFixture(t, namedFixtureDir(t, "gate_only"))

	code, stdout, stderr := runC2Q(t, bin, workDir, srv.URL, "deploy", "--env", "production", "--dry-run")
	combined := stdout + stderr

	assert.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	assert.Contains(t, combined, "can-i-deploy: api")
	assert.Contains(t, combined, "can-i-deploy: tts-speaker", "gate_only service must still be gated")
	assert.Contains(t, combined, "dry-run")
	assert.NotContains(t, combined, "no such service")
}
