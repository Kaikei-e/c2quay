//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_Deploy_DryRun_Pass runs `c2quay deploy --dry-run` against a fake
// broker that says "deployable". --dry-run stops before compose up, but the
// pipeline still captures a pre-deploy snapshot via `docker compose ps`
// first (see release.Deploy step 1), so this still requires a reachable
// Docker daemon. See requireDockerDaemon (gate_only_test.go).
func TestE2E_Deploy_DryRun_Pass(t *testing.T) {
	requireDockerDaemon(t)
	bin := buildBinary(t)
	srv := fakeBroker(t, true, "ok")

	// Create a working directory with the fixture so .c2quay/locks is isolated.
	workDir := t.TempDir()
	for _, f := range []string{"c2quay.yml", "compose.yaml", "versions.json"} {
		src, err := os.ReadFile(filepath.Join(fixtureDir(t), f))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(workDir, f), src, 0o644))
	}

	code, stdout, stderr := runC2Q(t, bin, workDir, srv.URL, "deploy", "--env", "production", "--dry-run")
	combined := stdout + stderr
	assert.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	assert.Contains(t, combined, "environment lock")
	assert.Contains(t, combined, "pre-snapshot")
	assert.Contains(t, combined, "can-i-deploy")
	assert.Contains(t, combined, "dry-run")

	// Lock should be released; pre-snapshot should exist.
	_, err := os.Stat(filepath.Join(workDir, ".c2quay", "locks", "production.lock"))
	assert.NoError(t, err, "lock file created")
	entries, _ := os.ReadDir(filepath.Join(workDir, ".c2quay", "snapshots"))
	assert.NotEmpty(t, entries, "pre snapshot written")
}

// TestE2E_Deploy_GateFailed exits 1 and does not write a post-snapshot.
// Also requires Docker for the same reason as TestE2E_Deploy_DryRun_Pass:
// the pre-deploy snapshot runs before the gate check.
func TestE2E_Deploy_GateFailed(t *testing.T) {
	requireDockerDaemon(t)
	bin := buildBinary(t)
	srv := fakeBroker(t, false, "contracts not compatible")

	workDir := t.TempDir()
	for _, f := range []string{"c2quay.yml", "compose.yaml", "versions.json"} {
		src, err := os.ReadFile(filepath.Join(fixtureDir(t), f))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(workDir, f), src, 0o644))
	}

	code, stdout, stderr := runC2Q(t, bin, workDir, srv.URL, "deploy", "--env", "production", "--dry-run")
	combined := stdout + stderr
	assert.Equal(t, 1, code, "gate failure must exit 1, got stdout=%s stderr=%s", stdout, stderr)
	assert.Contains(t, combined, "Rollback hints")
	assert.Contains(t, combined, "gate")
}
