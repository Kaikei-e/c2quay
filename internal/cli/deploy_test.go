package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/release"
)

// --- classifyDeployError -------------------------------------------------

// TestClassifyDeployError_GateFailure_ExitsOne proves the one exit code the
// operator relies on to script around: a gate rejection (report.FailedAtStep
// == StepGate, err wraps broker.ErrGateFailed) must map to ExitGateFailed
// (1), distinct from every other failure mode.
func TestClassifyDeployError_GateFailure_ExitsOne(t *testing.T) {
	report := &release.DeployReport{FailedAtStep: release.StepGate}
	err := classifyDeployError(broker.ErrGateFailed, report)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitGateFailed, ee.Code)
}

// TestClassifyDeployError_GateFailure_WrappedError proves errors.Is still
// matches when the gate error is wrapped (as release.Deploy actually returns
// it: fmt.Errorf("%w: %s", broker.ErrGateFailed, reason)).
func TestClassifyDeployError_GateFailure_WrappedError(t *testing.T) {
	report := &release.DeployReport{FailedAtStep: release.StepGate}
	wrapped := errors.Join(broker.ErrGateFailed, errors.New("contracts not compatible"))
	err := classifyDeployError(wrapped, report)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitGateFailed, ee.Code)
}

func TestClassifyDeployError_ComposeUpFailure_ExitsTwo(t *testing.T) {
	report := &release.DeployReport{FailedAtStep: release.StepComposeUp}
	err := classifyDeployError(errors.New("compose up: exit 1"), report)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

func TestClassifyDeployError_SmokeFailure_ExitsTwo(t *testing.T) {
	report := &release.DeployReport{FailedAtStep: release.StepSmoke}
	err := classifyDeployError(errors.New("smoke test failed"), report)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

func TestClassifyDeployError_RecordFailure_ExitsTwo(t *testing.T) {
	report := &release.DeployReport{FailedAtStep: release.StepRecord}
	err := classifyDeployError(errors.New("record-deployment: 1/2 recorded"), report)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

// TestClassifyDeployError_NilReport_ExitsTwo proves a nil report (an
// operator-level error that never reached Deploy's step tracking, e.g.
// DeployDeps validation) still classifies safely instead of panicking on the
// report.FailedAtStep dereference.
func TestClassifyDeployError_NilReport_ExitsTwo(t *testing.T) {
	err := classifyDeployError(errors.New("DeployDeps.Broker is required"), nil)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

// TestClassifyDeployError_GateStepButNotGateFailedErr_ExitsTwo proves the
// exit-1 mapping requires BOTH FailedAtStep == StepGate AND errors.Is(...,
// ErrGateFailed) — a gate step that failed for an operator-level reason
// (e.g. the broker was unreachable during the check, which GateAll surfaces
// as o.Err rather than a "not deployable" verdict) must not masquerade as a
// deliberate gate rejection.
func TestClassifyDeployError_GateStepButNotGateFailedErr_ExitsTwo(t *testing.T) {
	report := &release.DeployReport{FailedAtStep: release.StepGate}
	err := classifyDeployError(errors.New("broker unreachable"), report)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
}

// --- writeDeployFailureHint ----------------------------------------------

// TestWriteDeployFailureHint_RecordFailure_IncludesRecordResults proves the
// CLI wiring correctly threads report.RecordResults into the RollbackHint it
// prints — this is the seam covering 32d84c6's itemized record-deployment
// failure reporting at the command layer (release/rollback_hint_test.go
// already covers RollbackHint.Write's own rendering logic).
func TestWriteDeployFailureHint_RecordFailure_IncludesRecordResults(t *testing.T) {
	results := []release.RecordResult{
		{Service: "api", Pacticipant: "api", Version: "v1", Recorded: true},
		{Service: "web", Pacticipant: "web", Version: "v2", Recorded: false, Err: errors.New("broker 503")},
	}
	report := &release.DeployReport{
		FailedAtStep:  release.StepRecord,
		FailedCause:   &release.RecordDeploymentError{Results: results},
		RecordResults: results,
	}

	var buf bytes.Buffer
	writeDeployFailureHint(&buf, "production", report)

	out := buf.String()
	assert.Contains(t, out, "production")
	assert.Contains(t, out, "record-deployment results")
	assert.Contains(t, out, "api@v1")
	assert.Contains(t, out, "web@v2")
	assert.Contains(t, out, "broker 503")
	assert.NotContains(t, out, "record-deployment was NOT called for the failed attempt")
}

// TestWriteDeployFailureHint_NonRecordFailure_OmitsRecordResults proves a
// failure before the record step (e.g. compose-up) keeps printing the
// blanket "not called" note rather than an empty/misleading itemization,
// even though report.RecordResults happens to be nil.
func TestWriteDeployFailureHint_NonRecordFailure_OmitsRecordResults(t *testing.T) {
	report := &release.DeployReport{
		FailedAtStep: release.StepComposeUp,
		FailedCause:  errors.New("compose up: exit 1"),
	}

	var buf bytes.Buffer
	writeDeployFailureHint(&buf, "staging", report)

	out := buf.String()
	assert.Contains(t, out, "staging")
	assert.Contains(t, out, "compose-up")
	assert.Contains(t, out, "record-deployment was NOT called for the failed attempt")
}

// TestWriteDeployFailureHint_IncludesSnapshotsAndRollback proves every
// report field runDeploy has available (pre/post snapshot, snapshot file,
// rollback report) actually reaches the hint, not just FailedAtStep/Cause.
func TestWriteDeployFailureHint_IncludesSnapshotsAndRollback(t *testing.T) {
	pre := &release.Snapshot{Env: "production", Images: map[string]string{"api": "registry/api:old"}}
	report := &release.DeployReport{
		FailedAtStep:    release.StepComposeUp,
		FailedCause:     errors.New("compose up: exit 1"),
		Pre:             pre,
		PreSnapshotFile: "/tmp/pre.json",
		Rollback: &release.RollbackReport{
			Mode:      release.RollbackOn,
			Attempted: true,
			Succeeded: true,
		},
	}

	var buf bytes.Buffer
	writeDeployFailureHint(&buf, "production", report)

	out := buf.String()
	assert.Contains(t, out, "/tmp/pre.json")
	assert.Contains(t, out, "restored to their pre-deploy images")
}

// --- newDeployCommand: required-flag validation ---------------------------

// TestNewDeployCommand_MissingEnv_FailsBeforeAnyIO proves --env is checked
// before runDeploy is ever called — this is what lets the test avoid a real
// broker/docker/lock dependency entirely.
func TestNewDeployCommand_MissingEnv_FailsBeforeAnyIO(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newDeployCommand(rt)
	cmd.SetArgs([]string{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	assert.Contains(t, err.Error(), "--env is required")
}

// TestNewDeployCommand_InvalidAutoRollback_FailsBeforeAnyIO proves an
// invalid --auto-rollback value is rejected by ParseRollbackMode inside
// RunE before runDeploy is reached, so this too needs no broker/docker.
//
// --env is a persistent flag registered on the root command (see root.go);
// newDeployCommand built standalone (as in every test in this file) does not
// have it locally, so it is set directly on rt.flags — exactly what root's
// PersistentFlags binding would have done — rather than via cmd.SetArgs.
func TestNewDeployCommand_InvalidAutoRollback_FailsBeforeAnyIO(t *testing.T) {
	rt, _, _ := newTestRuntime()
	rt.flags.envName = "production"
	cmd := newDeployCommand(rt)
	cmd.SetArgs([]string{"--auto-rollback", "sideways"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	assert.Contains(t, err.Error(), "invalid --auto-rollback value")
}

// --- newDeployCommand: flag parsing ---------------------------------------

// TestNewDeployCommand_FlagDefaults locks the documented defaults for every
// deploy flag, read back via cmd.Flags() (not the unexported closure
// variables) so the test exercises the same binding cobra itself uses.
func TestNewDeployCommand_FlagDefaults(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newDeployCommand(rt)

	cases := map[string]string{
		"service":        "",
		"dry-run":        "false",
		"auto-rollback":  "on",
		"force-recreate": "false",
	}
	for name, want := range cases {
		f := cmd.Flags().Lookup(name)
		require.NotNilf(t, f, "flag %q must be registered", name)
		assert.Equalf(t, want, f.DefValue, "flag %q default", name)
	}
}

// TestNewDeployCommand_ParseFlags_BindsEveryValue parses a full set of
// flags and reads them back through cmd.Flags() — pflag updates the Value
// backing the *StringVar/*BoolVar targets during Parse, so this proves the
// wiring end to end without ever calling RunE (and therefore without
// touching lock/docker/broker).
func TestNewDeployCommand_ParseFlags_BindsEveryValue(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newDeployCommand(rt)

	require.NoError(t, cmd.ParseFlags([]string{
		"--service", "web",
		"--dry-run",
		"--auto-rollback", "dry-run",
		"--force-recreate",
	}))

	assert.Equal(t, "web", cmd.Flags().Lookup("service").Value.String())
	assert.Equal(t, "true", cmd.Flags().Lookup("dry-run").Value.String())
	assert.Equal(t, "dry-run", cmd.Flags().Lookup("auto-rollback").Value.String())
	assert.Equal(t, "true", cmd.Flags().Lookup("force-recreate").Value.String())
}

// TestNewDeployCommand_ParseFlags_ShortAutoRollbackValues proves every
// accepted spelling for --auto-rollback (see release.ParseRollbackMode)
// parses at the flag level without error; RunE's ParseRollbackMode call is
// what normalises them, tested separately in the versioning/release package.
func TestNewDeployCommand_ParseFlags_ShortAutoRollbackValues(t *testing.T) {
	for _, v := range []string{"on", "off", "dry-run"} {
		t.Run(v, func(t *testing.T) {
			rt, _, _ := newTestRuntime()
			cmd := newDeployCommand(rt)
			require.NoError(t, cmd.ParseFlags([]string{"--auto-rollback", v}))
			assert.Equal(t, v, cmd.Flags().Lookup("auto-rollback").Value.String())
		})
	}
}
