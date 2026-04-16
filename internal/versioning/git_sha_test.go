package versioning

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseShortOption(t *testing.T) {
	cases := []struct {
		in           string
		wantShort    int
		wantRecognised bool
		wantErr      bool
	}{
		{"", 0, false, false},
		{"true", -1, true, false},
		{"TRUE", -1, true, false},
		{"yes", -1, true, false},
		{"on", -1, true, false},
		{"false", 0, true, false},
		{"no", 0, true, false},
		{"off", 0, true, false},
		{"0", 0, true, false},
		{"7", 7, true, false},
		{"12", 12, true, false},
		{"40", 40, true, false},
		{"41", 0, false, true},  // too long
		{"-1", 0, false, true},  // negative via string rejected
		{"abc", 0, false, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, ok, err := ParseShortOption(c.in)
			if c.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.wantShort, got)
			assert.Equal(t, c.wantRecognised, ok)
		})
	}
}

// fakeRunner records the args it was invoked with and returns a canned output.
type fakeRunner struct {
	args []string
	out  string
	err  error
}

func (f *fakeRunner) run(_ context.Context, _ string, args ...string) (string, error) {
	f.args = args
	return f.out, f.err
}

func TestGitSHA_Resolve_FullSHAByDefault(t *testing.T) {
	r := &fakeRunner{out: "0123456789abcdef0123456789abcdef01234567"}
	g := NewGitSHA()
	g.runner = r.run

	rel, err := g.Resolve(context.Background(), []string{"api"})
	require.NoError(t, err)
	assert.Equal(t, r.out, rel["api"].Version)
	assert.Equal(t, []string{"rev-parse", "HEAD"}, r.args, "default run must not pass --short")
}

func TestGitSHA_Resolve_ShortTrueUsesGitDefault(t *testing.T) {
	r := &fakeRunner{out: "0123456"}
	g := NewGitSHAWith(GitSHAOptions{Short: -1})
	g.runner = r.run

	rel, err := g.Resolve(context.Background(), []string{"api"})
	require.NoError(t, err)
	assert.Equal(t, "0123456", rel["api"].Version)
	assert.Equal(t, []string{"rev-parse", "--short", "HEAD"}, r.args)
}

func TestGitSHA_Resolve_ShortExplicitLength(t *testing.T) {
	r := &fakeRunner{out: "012345678901"}
	g := NewGitSHAWith(GitSHAOptions{Short: 12})
	g.runner = r.run

	rel, err := g.Resolve(context.Background(), []string{"api", "worker"})
	require.NoError(t, err)
	assert.Equal(t, "012345678901", rel["api"].Version)
	assert.Equal(t, "012345678901", rel["worker"].Version)
	assert.Equal(t, []string{"rev-parse", "--short=12", "HEAD"}, r.args)
}

func TestGitSHA_Resolve_GitFailure(t *testing.T) {
	r := &fakeRunner{err: errors.New("not a git repository")}
	g := NewGitSHA()
	g.runner = r.run

	_, err := g.Resolve(context.Background(), []string{"api"})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "git_sha:"))
}

func TestGitSHA_Resolve_EmptyOutput(t *testing.T) {
	// defaultRunner trims whitespace, so the invariant seen by Resolve is "trimmed string".
	// The empty-after-trim branch is what we exercise here.
	r := &fakeRunner{out: ""}
	g := NewGitSHA()
	g.runner = r.run

	_, err := g.Resolve(context.Background(), []string{"api"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty SHA")
}
