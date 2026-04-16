package versioning_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

type stubAdapter struct {
	rc *composeadapter.RenderedConfig
}

func (s *stubAdapter) Version(context.Context) (composeadapter.VersionInfo, error) {
	return composeadapter.VersionInfo{}, nil
}
func (s *stubAdapter) Validate(context.Context) error { return nil }
func (s *stubAdapter) RenderConfigJSON(context.Context) (*composeadapter.RenderedConfig, error) {
	return s.rc, nil
}
func (s *stubAdapter) PsJSON(context.Context) ([]composeadapter.ContainerStatus, error) {
	return nil, nil
}
func (s *stubAdapter) Up(context.Context, composeadapter.UpOptions, io.Writer) error { return nil }

var _ composeadapter.Adapter = (*stubAdapter)(nil)

func TestResolvedDigest_DigestOK(t *testing.T) {
	a := &stubAdapter{rc: &composeadapter.RenderedConfig{
		Services: map[string]composeadapter.RenderedService{
			"api": {Image: "ghcr.io/example/api@sha256:1111111111111111"},
		},
	}}
	r := versioning.NewResolvedDigest(a)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := r.Resolve(ctx, []string{"api"})
	require.NoError(t, err)
	assert.Equal(t, "sha256:1111111111111111", out["api"].Version)
}

func TestResolvedDigest_TagOnlyRejected(t *testing.T) {
	a := &stubAdapter{rc: &composeadapter.RenderedConfig{
		Services: map[string]composeadapter.RenderedService{
			"api": {Image: "ghcr.io/example/api:latest"},
		},
	}}
	r := versioning.NewResolvedDigest(a)
	_, err := r.Resolve(context.Background(), []string{"api"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tag-only")
}

func TestResolvedDigest_MissingService(t *testing.T) {
	a := &stubAdapter{rc: &composeadapter.RenderedConfig{
		Services: map[string]composeadapter.RenderedService{},
	}}
	r := versioning.NewResolvedDigest(a)
	_, err := r.Resolve(context.Background(), []string{"api"})
	require.Error(t, err)
}
