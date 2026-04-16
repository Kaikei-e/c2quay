package doctor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/doctor"
)

type scriptedRunner struct {
	responses map[string]response
}

type response struct {
	out []byte
	err error
}

func key(name string, args []string) string {
	k := name
	for _, a := range args {
		k += " " + a
	}
	return k
}

func (s scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if r, ok := s.responses[key(name, args)]; ok {
		return r.out, r.err
	}
	return nil, errors.New("unexpected command: " + key(name, args))
}

func TestParseComposeVersion(t *testing.T) {
	cases := []struct {
		in   string
		want doctor.ComposeVersion
		err  bool
	}{
		{"v2.40.2", doctor.ComposeVersion{2, 40, 2}, false},
		{"2.40.2", doctor.ComposeVersion{2, 40, 2}, false},
		{"Docker Compose version v2.40.2", doctor.ComposeVersion{2, 40, 2}, false},
		{"v2.40.2-desktop.1", doctor.ComposeVersion{2, 40, 2}, false},
		{"v5.0.0+build.1", doctor.ComposeVersion{5, 0, 0}, false},
		{"garbage", doctor.ComposeVersion{}, true},
	}
	for _, tc := range cases {
		got, err := doctor.ParseComposeVersion(tc.in)
		if tc.err {
			assert.Error(t, err, tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}

func TestVersionOrdering(t *testing.T) {
	assert.True(t, doctor.ComposeVersion{2, 40, 1}.LessThan(doctor.ComposeVersion{2, 40, 2}))
	assert.False(t, doctor.ComposeVersion{2, 40, 2}.LessThan(doctor.ComposeVersion{2, 40, 2}))
	assert.False(t, doctor.ComposeVersion{2, 41, 0}.LessThan(doctor.ComposeVersion{2, 40, 2}))
	assert.False(t, doctor.ComposeVersion{5, 0, 0}.LessThan(doctor.ComposeVersion{2, 40, 2}))
}

func TestRun_AllGreen(t *testing.T) {
	runner := scriptedRunner{responses: map[string]response{
		key("docker", []string{"info", "--format", "{{.ServerVersion}}"}):         {out: []byte("29.4.0\n")},
		key("docker-compose", []string{"version", "--short"}):                     {err: errors.New("not found")},
		key("docker", []string{"compose", "version", "--format", "json"}):         {out: []byte(`{"version":"v2.40.2"}`)},
	}}
	r := doctor.Run(context.Background(), runner)
	assert.True(t, r.AllOK(), "unexpected failure: %+v", r.Results)
}

func TestRun_HyphenatedComposeInstalled(t *testing.T) {
	runner := scriptedRunner{responses: map[string]response{
		key("docker", []string{"info", "--format", "{{.ServerVersion}}"}):         {out: []byte("29.4.0\n")},
		key("docker-compose", []string{"version", "--short"}):                     {out: []byte("1.29.2")},
		key("docker", []string{"compose", "version", "--format", "json"}):         {out: []byte(`{"version":"v2.40.2"}`)},
	}}
	r := doctor.Run(context.Background(), runner)
	assert.False(t, r.AllOK())
	// The hyphen check must be the one that failed.
	for _, res := range r.Results {
		if res.Name == "docker-compose absent" {
			assert.False(t, res.OK)
			return
		}
	}
	t.Fatalf("hyphen check not found in %+v", r.Results)
}

func TestRun_ComposeTooOld(t *testing.T) {
	runner := scriptedRunner{responses: map[string]response{
		key("docker", []string{"info", "--format", "{{.ServerVersion}}"}):         {out: []byte("29.4.0")},
		key("docker-compose", []string{"version", "--short"}):                     {err: errors.New("not found")},
		key("docker", []string{"compose", "version", "--format", "json"}):         {out: []byte(`{"version":"v2.40.1"}`)},
	}}
	r := doctor.Run(context.Background(), runner)
	assert.False(t, r.AllOK())
}
