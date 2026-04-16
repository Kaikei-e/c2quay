package composeadapter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
)

func TestParseRenderedConfig(t *testing.T) {
	raw := []byte(`{
  "name": "myapp",
  "services": {
    "api": {"image": "ghcr.io/example/api@sha256:abc"},
    "web": {"image": "ghcr.io/example/web:latest"},
    "sidecar": {}
  }
}`)
	rc, err := composeadapter.ParseRenderedConfig(raw)
	require.NoError(t, err)
	assert.Equal(t, "myapp", rc.Name)
	imgs := rc.ImagesByService()
	assert.Equal(t, "ghcr.io/example/api@sha256:abc", imgs["api"])
	assert.Equal(t, "ghcr.io/example/web:latest", imgs["web"])
	_, hasSidecar := imgs["sidecar"]
	assert.False(t, hasSidecar, "services without image should be skipped")
}
