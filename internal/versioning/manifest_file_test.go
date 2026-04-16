package versioning_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/versioning"
)

func TestManifestFile_Resolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "versions.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
"services":{
  "api":{"version":"2026-04-17-abc","image":"ghcr.io/example/api@sha256:a"},
  "web":{"version":"2026-04-17-def","image":"ghcr.io/example/web@sha256:b"}
}}`), 0o644))

	m := versioning.NewManifestFile(path)
	out, err := m.Resolve(context.Background(), []string{"api", "web"})
	require.NoError(t, err)
	assert.Equal(t, "2026-04-17-abc", out["api"].Version)
	assert.Equal(t, "ghcr.io/example/api@sha256:a", out["api"].ImageRef)
}

func TestManifestFile_MissingService(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "versions.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"services":{"api":{"version":"v1"}}}`), 0o644))

	m := versioning.NewManifestFile(path)
	_, err := m.Resolve(context.Background(), []string{"api", "missing"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestManifestFile_FileNotFound(t *testing.T) {
	m := versioning.NewManifestFile("/tmp/definitely-does-not-exist-xyzzy.json")
	_, err := m.Resolve(context.Background(), []string{"api"})
	require.Error(t, err)
}
