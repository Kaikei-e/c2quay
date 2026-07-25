package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewVersionCommand_PrintsVersion(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	cmd := newVersionCommand(rt)
	cmd.SetArgs(nil)

	require.NoError(t, cmd.Execute())
	assert.Contains(t, stdout.String(), "c2quay ")
}

// TestResolveVersion_DevDefault_FallsBackToBuildInfo proves resolveVersion
// doesn't panic and returns a non-empty string even in the `go test`
// environment where debug.ReadBuildInfo's Main.Version is typically
// "(devel)" — the exact fallback path most CI runs actually exercise.
func TestResolveVersion_DevDefault_FallsBackToBuildInfo(t *testing.T) {
	assert.NotEmpty(t, resolveVersion())
}

// TestResolveVersion_LinkerSetVersion_TakesPriority proves that once the
// linker (or a test) sets Version away from "dev", resolveVersion returns it
// verbatim without consulting build info.
func TestResolveVersion_LinkerSetVersion_TakesPriority(t *testing.T) {
	old := Version
	Version = "v1.2.3"
	defer func() { Version = old }()

	assert.Equal(t, "v1.2.3", resolveVersion())
}
