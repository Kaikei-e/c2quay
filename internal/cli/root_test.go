package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewRootCommand_RegistersAllSubcommands proves every subcommand is
// wired into the root, so a future command that forgets AddCommand fails a
// test instead of silently vanishing from `c2quay --help`.
func TestNewRootCommand_RegistersAllSubcommands(t *testing.T) {
	root, rt := newRootCommand()
	require.NotNil(t, rt)

	want := []string{"version", "init", "doctor", "verify", "deploy", "rollback", "status"}
	var got []string
	for _, c := range root.Commands() {
		got = append(got, c.Name())
	}
	for _, name := range want {
		assert.Contains(t, got, name)
	}
}

// TestNewRootCommand_PersistentFlagDefaults locks the documented defaults
// for the global flags (config path, output format, log level) so a change
// there is deliberate, not accidental.
func TestNewRootCommand_PersistentFlagDefaults(t *testing.T) {
	root, _ := newRootCommand()
	flags := root.PersistentFlags()

	cases := map[string]string{
		"config":    "c2quay.yml",
		"env":       "",
		"output":    "text",
		"log-level": "info",
		"audit-log": "",
	}
	for name, want := range cases {
		f := flags.Lookup(name)
		require.NotNilf(t, f, "flag %q must be registered", name)
		assert.Equalf(t, want, f.DefValue, "flag %q default value", name)
	}
}

// TestNewRootCommand_SilencesUsageAndErrors proves the root command opts out
// of cobra's default usage-dump-on-error behaviour, which classifyError
// relies on to keep stderr limited to a single "error: ..." line.
func TestNewRootCommand_SilencesUsageAndErrors(t *testing.T) {
	root, _ := newRootCommand()
	assert.True(t, root.SilenceUsage)
	assert.True(t, root.SilenceErrors)
}

func TestClassifyError_ExitErrorPropagatesCode(t *testing.T) {
	var stderr bytes.Buffer
	code := classifyError(&ExitError{Code: ExitGateFailed, Err: errors.New("gate said no")}, &stderr)
	assert.Equal(t, ExitGateFailed, code)
	assert.Contains(t, stderr.String(), "gate said no")
}

// TestClassifyError_WrappedExitErrorStillUnwraps proves errors.As reaches
// through a wrapping error (e.g. fmt.Errorf("...: %w", exitErr)) to recover
// the intended exit code.
func TestClassifyError_WrappedExitErrorStillUnwraps(t *testing.T) {
	inner := &ExitError{Code: ExitGateFailed, Err: errors.New("gate said no")}
	var stderr bytes.Buffer
	code := classifyError(wrapErr{inner}, &stderr)
	assert.Equal(t, ExitGateFailed, code)
}

type wrapErr struct{ err error }

func (w wrapErr) Error() string { return "wrapped: " + w.err.Error() }
func (w wrapErr) Unwrap() error { return w.err }

// TestClassifyError_PlainErrorDefaultsToOperatorError proves any error that
// does not implement exitCoder falls back to ExitOperatorError (2), never a
// zero/success code.
func TestClassifyError_PlainErrorDefaultsToOperatorError(t *testing.T) {
	var stderr bytes.Buffer
	code := classifyError(errors.New("boom"), &stderr)
	assert.Equal(t, ExitOperatorError, code)
	assert.Contains(t, stderr.String(), "error: boom")
}

func TestExitError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("root cause")
	ee := &ExitError{Code: ExitOperatorError, Err: cause}
	assert.Equal(t, "root cause", ee.Error())
	assert.Equal(t, cause, errors.Unwrap(ee))
	assert.Equal(t, ExitOperatorError, ee.ExitCode())
}

// TestExecute_UnknownCommand_ReturnsOperatorError drives Execute end to end
// (the real entrypoint main() calls) with an invalid subcommand, which
// cobra rejects before touching any I/O.
func TestExecute_UnknownCommand_ReturnsOperatorError(t *testing.T) {
	// Execute reads os.Args indirectly via cobra's default; instead exercise
	// the same code path through a root command built the same way, since
	// Execute() itself has no seam for injecting args (it always parses
	// os.Args via root.ExecuteContext). We cover Execute's exit-code mapping
	// via classifyError above and confirm here that root command construction
	// plus a bogus command name still surfaces a non-zero, non-gate exit
	// through the same ExecuteContext path Execute() uses.
	root, _ := newRootCommand()
	root.SetContext(context.Background())
	root.SetArgs([]string{"not-a-real-command"})
	var stderr bytes.Buffer
	root.SetOut(&stderr)
	root.SetErr(&stderr)
	err := root.ExecuteContext(context.Background())
	require.Error(t, err)
	code := classifyError(err, &bytes.Buffer{})
	assert.Equal(t, ExitOperatorError, code)
}
