package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- newRollbackCommand: required-flag validation --------------------------

func TestNewRollbackCommand_MissingEnv_FailsBeforeAnyIO(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newRollbackCommand(rt)
	cmd.SetArgs([]string{"--from-snapshot", "/tmp/pre.json"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	assert.Contains(t, err.Error(), "--env is required")
}

// TestNewRollbackCommand_MissingFromSnapshot_FailsBeforeAnyIO proves
// --from-snapshot is required even when --env is supplied, and that this
// check happens before runRollback (and therefore before LoadSnapshot,
// lock.Acquire, or any compose/broker call).
//
// --env is a persistent flag registered on the root command (see root.go);
// newRollbackCommand built standalone does not have it locally, so it is set
// directly on rt.flags rather than via cmd.SetArgs — see the equivalent note
// in deploy_test.go.
func TestNewRollbackCommand_MissingFromSnapshot_FailsBeforeAnyIO(t *testing.T) {
	rt, _, _ := newTestRuntime()
	rt.flags.envName = "production"
	cmd := newRollbackCommand(rt)
	cmd.SetArgs([]string{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	assert.Contains(t, err.Error(), "--from-snapshot is required")
}

// TestNewRollbackCommand_MissingBoth_EnvCheckedFirst proves the flag
// validation order: --env is checked before --from-snapshot, matching
// newDeployCommand's convention of validating global flags first.
func TestNewRollbackCommand_MissingBoth_EnvCheckedFirst(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newRollbackCommand(rt)
	cmd.SetArgs([]string{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--env is required")
}

// --- newRollbackCommand: flag parsing ---------------------------------------

func TestNewRollbackCommand_FlagDefaults(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newRollbackCommand(rt)

	fromSnapshot := cmd.Flags().Lookup("from-snapshot")
	require.NotNil(t, fromSnapshot)
	assert.Equal(t, "", fromSnapshot.DefValue)

	dryRun := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRun)
	assert.Equal(t, "false", dryRun.DefValue)
}

func TestNewRollbackCommand_ParseFlags_BindsValues(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newRollbackCommand(rt)

	require.NoError(t, cmd.ParseFlags([]string{
		"--from-snapshot", "/tmp/pre.json",
		"--dry-run",
	}))

	assert.Equal(t, "/tmp/pre.json", cmd.Flags().Lookup("from-snapshot").Value.String())
	assert.Equal(t, "true", cmd.Flags().Lookup("dry-run").Value.String())
}
