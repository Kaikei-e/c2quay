package versioning

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GitSHAOptions tunes the git_sha strategy. Zero value = full 40-char SHA,
// which preserves pre-v0.4.5 behaviour.
type GitSHAOptions struct {
	// Short asks git for an abbreviated SHA. 0 means full; values > 0 pin an
	// explicit length (passed as `--short=N`); -1 means "use git's default
	// abbreviation" (honours core.abbrev, usually 7).
	Short int
}

type GitSHA struct {
	opts   GitSHAOptions
	runner func(ctx context.Context, name string, args ...string) (string, error)
}

func NewGitSHA() *GitSHA {
	return &GitSHA{runner: defaultRunner}
}

// NewGitSHAWith constructs a GitSHA strategy with explicit options. Used by
// the Factory when the config asks for an abbreviated SHA.
func NewGitSHAWith(opts GitSHAOptions) *GitSHA {
	return &GitSHA{opts: opts, runner: defaultRunner}
}

func defaultRunner(ctx context.Context, name string, args ...string) (string, error) {
	var out, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, errBuf.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func (*GitSHA) Name() string { return "git_sha" }

func (g *GitSHA) Resolve(ctx context.Context, services []string) (map[string]Release, error) {
	args := []string{"rev-parse"}
	switch {
	case g.opts.Short < 0:
		args = append(args, "--short") // let git pick the length (core.abbrev)
	case g.opts.Short > 0:
		args = append(args, fmt.Sprintf("--short=%d", g.opts.Short))
	}
	args = append(args, "HEAD")

	sha, err := g.runner(ctx, "git", args...)
	if err != nil {
		return nil, fmt.Errorf("git_sha: %w", err)
	}
	if sha == "" {
		return nil, fmt.Errorf("git_sha: empty SHA from git %s", strings.Join(args, " "))
	}
	out := make(map[string]Release, len(services))
	for _, s := range services {
		out[s] = Release{Version: sha}
	}
	return out, nil
}

// ParseShortOption interprets the raw value of versioning.options.short from
// YAML (which is always a string in the map[string]string model). It accepts
// common boolean spellings and positive integers.
//
// Returns (short, recognized, error):
//
//	""                      → (0, false, nil)   // absent, caller uses default
//	"false"/"0"/"no"        → (0, true, nil)    // explicit off
//	"true"/"yes"            → (-1, true, nil)   // use git default abbreviation
//	"N" where N in [1..40]  → (N, true, nil)    // explicit length
//	anything else           → error
func ParseShortOption(raw string) (int, bool, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return 0, false, nil
	}
	switch trimmed {
	case "true", "yes", "on":
		return -1, true, nil
	case "false", "no", "off", "0":
		return 0, true, nil
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n < 1 || n > 40 {
		return 0, false, fmt.Errorf("versioning.options.short: %q is not a boolean or an integer in [1,40]", raw)
	}
	return n, true, nil
}
