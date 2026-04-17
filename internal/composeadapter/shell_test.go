package composeadapter_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

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
}

func key(name string, args []string) string {
	return name + " " + strings.Join(args, " ")
}

func (f *fakeExec) Output(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if r, ok := f.outputs[key(name, args)]; ok {
		return r.stdout, r.stderr, r.err
	}
	return nil, nil, errors.New("unexpected: " + key(name, args))
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
	fe := &fakeExec{
		outputs: map[string]fakeResponse{
			key("docker", []string{"compose", "-f", "c.yml", "-p", "proj", "ps", "--all", "--format", "json"}): {
				stdout: []byte(`[{"Service":"api","State":"running","Health":"healthy"},{"Service":"init","State":"exited","ExitCode":0}]`),
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
	require.NoError(t, err, "false-positive exit 1 should be masked when ps shows healthy")
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
