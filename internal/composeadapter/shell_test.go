package composeadapter_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
)

type fakeResponse struct {
	stdout []byte
	stderr []byte
	err    error
}

type fakeExec struct {
	calls       [][]string
	outputs     map[string]fakeResponse
	streamed    [][]string
	streamedErr error

	// outputsSeq lets a test script successive responses for the SAME
	// command (e.g. two `ps` calls 2s apart returning different health).
	// Each call to that key pops the next entry; calls past the end repeat
	// the last entry. Falls back to outputs when the key isn't present here.
	outputsSeq map[string][]fakeResponse
	seqIdx     map[string]int
}

func key(name string, args []string) string {
	return name + " " + strings.Join(args, " ")
}

func (f *fakeExec) Output(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	k := key(name, args)
	if seq, ok := f.outputsSeq[k]; ok && len(seq) > 0 {
		if f.seqIdx == nil {
			f.seqIdx = map[string]int{}
		}
		idx := f.seqIdx[k]
		if idx >= len(seq) {
			idx = len(seq) - 1
		}
		f.seqIdx[k] = idx + 1
		r := seq[idx]
		return r.stdout, r.stderr, r.err
	}
	if r, ok := f.outputs[k]; ok {
		return r.stdout, r.stderr, r.err
	}
	return nil, nil, errors.New("unexpected: " + k)
}

func (f *fakeExec) RunWithStream(_ context.Context, w io.Writer, name string, args ...string) error {
	f.streamed = append(f.streamed, append([]string{name}, args...))
	_, _ = io.Copy(w, strings.NewReader("(simulated output)\n"))
	return f.streamedErr
}

func TestShellAdapter_Version(t *testing.T) {
	fe := &fakeExec{outputs: map[string]fakeResponse{
		key("docker", []string{"compose", "version", "--format", "json"}): {
			stdout: []byte(`{"version":"v2.40.2"}`),
		},
	}}
	a := composeadapter.NewShell(composeadapter.ShellOptions{Exec: fe})
	vi, err := a.Version(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v2.40.2", vi.Parsed.String())
	assert.True(t, vi.SupportsWait)
}

func TestShellAdapter_VersionTooOld(t *testing.T) {
	fe := &fakeExec{outputs: map[string]fakeResponse{
		key("docker", []string{"compose", "version", "--format", "json"}): {
			stdout: []byte(`{"version":"v2.40.1"}`),
		},
	}}
	a := composeadapter.NewShell(composeadapter.ShellOptions{Exec: fe})
	_, err := a.Version(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CVE-2025-62725")
}

func TestShellAdapter_Up_RunsExpectedCommand(t *testing.T) {
	fe := &fakeExec{outputs: map[string]fakeResponse{
		key("docker", []string{"compose", "-f", "compose.yaml", "-p", "app", "ps", "--all", "--format", "json"}): {
			stdout: []byte(`[{"Service":"api","State":"running","Health":"healthy"}]`),
		},
	}}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"compose.yaml"},
		ProjectName:  "app",
		Exec:         fe,
	})
	var progress bytes.Buffer
	err := a.Up(context.Background(), composeadapter.UpOptions{
		RemoveOrphans: true,
		Wait:          true,
	}, &progress)
	require.NoError(t, err)
	require.Len(t, fe.streamed, 1)
	assert.Equal(t, []string{"docker", "compose", "-f", "compose.yaml", "-p", "app", "up", "-d", "--remove-orphans", "--wait"}, fe.streamed[0])
}

func TestShellAdapter_Up_WaitFalsePositiveCompensated(t *testing.T) {
	// docker/compose#10596: --wait exits 1 even though everything is up.
	// Both health re-checks (2s apart in production, near-instant here) must
	// see the same healthy state before the exit error is overridden.
	fe := &fakeExec{
		outputs: map[string]fakeResponse{
			key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "ps", "--all", "--format", "json"}): {
				stdout: []byte(`[{"Service":"api","State":"running","Health":"healthy"},{"Service":"init","State":"exited","ExitCode":0}]`),
			},
		},
		streamedErr: errors.New("exit status 1"),
	}
	var progress bytes.Buffer
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles:       []string{"c.yml"},
		ProjectName:        "proj",
		Exec:               fe,
		HealthRecheckDelay: time.Millisecond,
	})
	err := a.Up(context.Background(), composeadapter.UpOptions{Wait: true}, &progress)
	require.NoError(t, err, "false-positive exit 1 should be masked when both ps re-checks show healthy")
	assert.Contains(t, progress.String(), "docker/compose#10596",
		"operators must see in Progress output that the exit error was overridden")
}

// TestShellAdapter_Up_TransientHealthyBlipNotMasked proves the tightened
// workaround: a FIRST ps check that looks healthy is not enough on its own.
// If the SECOND check (2s later in production) shows things have gone
// unhealthy, the original compose exit error must NOT be masked.
func TestShellAdapter_Up_TransientHealthyBlipNotMasked(t *testing.T) {
	psKey := key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "ps", "--all", "--format", "json"})
	fe := &fakeExec{
		outputsSeq: map[string][]fakeResponse{
			psKey: {
				{stdout: []byte(`[{"Service":"api","State":"running","Health":"healthy"}]`)},
				{stdout: []byte(`[{"Service":"api","State":"exited","ExitCode":1}]`)},
			},
		},
		streamedErr: errors.New("exit status 1"),
	}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles:       []string{"c.yml"},
		ProjectName:        "proj",
		Exec:               fe,
		HealthRecheckDelay: time.Millisecond,
	})
	err := a.Up(context.Background(), composeadapter.UpOptions{Wait: true}, io.Discard)
	require.Error(t, err, "a transient healthy blip that regresses on re-check must not be masked")
	assert.Contains(t, err.Error(), "exit status 1")

	psCalls := 0
	for _, c := range fe.calls {
		if len(c) > 0 && key(c[0], c[1:]) == psKey {
			psCalls++
		}
	}
	assert.Equal(t, 2, psCalls, "both health re-checks must have run")
}

// TestShellAdapter_Up_FirstCheckUnhealthy_SkipsSecondCheck proves we don't
// waste the recheck delay when the first check already shows unhealthy —
// the original error surfaces immediately.
func TestShellAdapter_Up_FirstCheckUnhealthy_SkipsSecondCheck(t *testing.T) {
	fe := &fakeExec{
		outputs: map[string]fakeResponse{
			key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "ps", "--all", "--format", "json"}): {
				stdout: []byte(`[{"Service":"api","State":"exited","ExitCode":1}]`),
			},
		},
		streamedErr: errors.New("exit status 1"),
	}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"c.yml"},
		ProjectName:  "proj",
		Exec:         fe,
		// Deliberately large: if the adapter incorrectly performed a second
		// check here, this test would time out the whole suite.
		HealthRecheckDelay: time.Hour,
	})
	start := time.Now()
	err := a.Up(context.Background(), composeadapter.UpOptions{Wait: true}, io.Discard)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "must not sleep the recheck delay when the first check already fails")
}

// TestShellAdapter_Up_SecondPsCheckErrors proves the second ps call failing
// outright (not just showing unhealthy) is surfaced as an error too, not
// silently treated as a pass.
func TestShellAdapter_Up_SecondPsCheckErrors(t *testing.T) {
	psKey := key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "ps", "--all", "--format", "json"})
	fe := &fakeExec{
		outputsSeq: map[string][]fakeResponse{
			psKey: {
				{stdout: []byte(`[{"Service":"api","State":"running","Health":"healthy"}]`)},
				{err: errors.New("docker daemon unreachable")},
			},
		},
		streamedErr: errors.New("exit status 1"),
	}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles:       []string{"c.yml"},
		ProjectName:        "proj",
		Exec:               fe,
		HealthRecheckDelay: time.Millisecond,
	})
	err := a.Up(context.Background(), composeadapter.UpOptions{Wait: true}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
	assert.Contains(t, err.Error(), "docker daemon unreachable")
}

// TestShellAdapter_Up_ContextCancelledDuringRecheck_DoesNotMask proves that
// if the context expires while waiting between the two health checks, the
// original compose error is returned rather than silently proceeding to a
// second check (or worse, treating the cancellation as success).
func TestShellAdapter_Up_ContextCancelledDuringRecheck_DoesNotMask(t *testing.T) {
	fe := &fakeExec{
		outputs: map[string]fakeResponse{
			key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "ps", "--all", "--format", "json"}): {
				stdout: []byte(`[{"Service":"api","State":"running","Health":"healthy"}]`),
			},
		},
		streamedErr: errors.New("exit status 1"),
	}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles:       []string{"c.yml"},
		ProjectName:        "proj",
		Exec:               fe,
		HealthRecheckDelay: 200 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := a.Up(ctx, composeadapter.UpOptions{Wait: true}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1", "original compose error must not be masked by ctx cancellation")

	psKey := key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "ps", "--all", "--format", "json"})
	psCalls := 0
	for _, c := range fe.calls {
		if len(c) > 0 && key(c[0], c[1:]) == psKey {
			psCalls++
		}
	}
	assert.Equal(t, 1, psCalls, "only the first health check should run before ctx expires")
}

func TestShellAdapter_Up_RealFailureNotMasked(t *testing.T) {
	fe := &fakeExec{
		outputs: map[string]fakeResponse{
			key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "ps", "--all", "--format", "json"}): {
				stdout: []byte(`[{"Service":"api","State":"exited","ExitCode":1}]`),
			},
		},
		streamedErr: errors.New("exit status 1"),
	}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"c.yml"},
		ProjectName:  "proj",
		Exec:         fe,
	})
	err := a.Up(context.Background(), composeadapter.UpOptions{Wait: true}, io.Discard)
	require.Error(t, err)
}

// TestShellAdapter_Up_ForceRecreate proves ADR 0011: UpOptions.ForceRecreate
// appends `--force-recreate` to the compose args.
func TestShellAdapter_Up_ForceRecreate(t *testing.T) {
	fe := &fakeExec{outputs: map[string]fakeResponse{
		key("docker", []string{"compose", "-f", "compose.yaml", "-p", "app", "ps", "--all", "--format", "json"}): {
			stdout: []byte(`[{"Service":"api","State":"running","Health":"healthy"}]`),
		},
	}}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"compose.yaml"},
		ProjectName:  "app",
		Exec:         fe,
	})
	err := a.Up(context.Background(), composeadapter.UpOptions{
		RemoveOrphans: true,
		ForceRecreate: true,
		Services:      []string{"api"},
	}, io.Discard)
	require.NoError(t, err)
	require.Len(t, fe.streamed, 1)
	assert.Equal(t,
		[]string{"docker", "compose", "-f", "compose.yaml", "-p", "app", "up", "-d", "--remove-orphans", "--force-recreate", "api"},
		fe.streamed[0])
}

// TestShellAdapter_Pull proves ADR 0010: Pull shells out to
// `docker compose pull <services>`.
func TestShellAdapter_Pull(t *testing.T) {
	fe := &fakeExec{}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"compose.yaml"},
		ProjectName:  "app",
		Exec:         fe,
	})
	err := a.Pull(context.Background(), []string{"api", "worker"}, io.Discard)
	require.NoError(t, err)
	require.Len(t, fe.streamed, 1)
	assert.Equal(t,
		[]string{"docker", "compose", "-f", "compose.yaml", "-p", "app", "pull", "api", "worker"},
		fe.streamed[0])
}

func TestShellAdapter_Pull_EmptyServicesPullsAll(t *testing.T) {
	fe := &fakeExec{}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"c.yml"},
		ProjectName:  "proj",
		Exec:         fe,
	})
	err := a.Pull(context.Background(), nil, io.Discard)
	require.NoError(t, err)
	require.Len(t, fe.streamed, 1)
	assert.Equal(t,
		[]string{"docker", "compose", "-f", "c.yml", "-p", "proj", "pull"},
		fe.streamed[0])
}

// TestShellAdapter_ConfigServices proves ConfigServices shells out to
// `docker compose config --services` and returns one entry per line. Used
// by plan-time gate_only coverage validation. See ADR 0013.
func TestShellAdapter_ConfigServices(t *testing.T) {
	fe := &fakeExec{outputs: map[string]fakeResponse{
		key("docker", []string{"compose", "-f", "compose.yaml", "-p", "app", "config", "--services"}): {
			stdout: []byte("api\nworker\n"),
		},
	}}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"compose.yaml"},
		ProjectName:  "app",
		Exec:         fe,
	})
	svcs, err := a.ConfigServices(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "worker"}, svcs)
}

func TestShellAdapter_ConfigServices_TrimsBlankLines(t *testing.T) {
	fe := &fakeExec{outputs: map[string]fakeResponse{
		key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "config", "--services"}): {
			stdout: []byte("api\n\nworker\n\n"),
		},
	}}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"c.yml"},
		ProjectName:  "proj",
		Exec:         fe,
	})
	svcs, err := a.ConfigServices(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "worker"}, svcs)
}

func TestShellAdapter_ConfigServices_WrapsErr(t *testing.T) {
	fe := &fakeExec{outputs: map[string]fakeResponse{
		key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "config", "--services"}): {
			err:    errors.New("exit status 1"),
			stderr: []byte("service \"x\" has neither an image nor a build context"),
		},
	}}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"c.yml"},
		ProjectName:  "proj",
		Exec:         fe,
	})
	_, err := a.ConfigServices(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker compose config --services failed")
}

func TestShellAdapter_Pull_WrapsErr(t *testing.T) {
	fe := &fakeExec{streamedErr: errors.New("registry 403")}
	a := composeadapter.NewShell(composeadapter.ShellOptions{
		ComposeFiles: []string{"c.yml"},
		ProjectName:  "proj",
		Exec:         fe,
	})
	err := a.Pull(context.Background(), []string{"api"}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker compose pull failed")
}
