package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/yaml.v3"
)

func newTestRuntime() (*runtimeCtx, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	rt := &runtimeCtx{stdout: &stdout, stderr: &stderr}
	return rt, &stdout, &stderr
}

// TestRunInit_WritesStarterConfig proves `c2quay init` is no longer a stub:
// it writes a starter c2quay.yml, and that file must be valid YAML shaped
// like config.example.yml (compose/broker/versioning/deploy/environments).
func TestRunInit_WritesStarterConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c2quay.yml")
	rt, stdout, _ := newTestRuntime()

	err := runInit(rt, path, false)
	require.NoError(t, err)

	raw, rerr := os.ReadFile(path)
	require.NoError(t, rerr)

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &doc), "starter config must be valid YAML")
	assert.Contains(t, doc, "compose")
	assert.Contains(t, doc, "broker")
	assert.Contains(t, doc, "versioning")
	assert.Contains(t, doc, "deploy")
	assert.Contains(t, doc, "environments")

	assert.Contains(t, stdout.String(), path)
	assert.Contains(t, stdout.String(), "Next steps")
}

// TestRunInit_RefusesToOverwriteExisting proves init never clobbers an
// existing config by default — the whole point of the "refuse if the file
// already exists" requirement.
func TestRunInit_RefusesToOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c2quay.yml")
	require.NoError(t, os.WriteFile(path, []byte("existing: true\n"), 0o600))
	rt, _, _ := newTestRuntime()

	err := runInit(rt, path, false)
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)

	raw, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, "existing: true\n", string(raw), "existing file must be left untouched")
}

// TestRunInit_ForceOverwritesExisting proves --force is the (only) escape
// hatch for the refuse-to-overwrite rule.
func TestRunInit_ForceOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c2quay.yml")
	require.NoError(t, os.WriteFile(path, []byte("existing: true\n"), 0o600))
	rt, _, _ := newTestRuntime()

	require.NoError(t, runInit(rt, path, true))

	raw, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Contains(t, string(raw), "compose:")
}

// TestNewInitCommand_ForceFlagWired proves the --force flag on the cobra
// command actually reaches runInit's overwrite behaviour end to end.
func TestNewInitCommand_ForceFlagWired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c2quay.yml")
	require.NoError(t, os.WriteFile(path, []byte("existing: true\n"), 0o600))

	rt, _, _ := newTestRuntime()
	rt.flags.configPath = path
	cmd := newInitCommand(rt)
	cmd.SetArgs([]string{"--force"})
	require.NoError(t, cmd.Execute())

	raw, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Contains(t, string(raw), "compose:")
}

// TestNewInitCommand_NoForce_RefusesAndExits proves the default (no
// --force) path through the real cobra command wiring.
func TestNewInitCommand_NoForce_RefusesAndExits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c2quay.yml")
	require.NoError(t, os.WriteFile(path, []byte("existing: true\n"), 0o600))

	rt, _, _ := newTestRuntime()
	rt.flags.configPath = path
	cmd := newInitCommand(rt)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	require.Error(t, err)
}
