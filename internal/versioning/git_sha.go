package versioning

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type GitSHA struct {
	runner func(ctx context.Context, name string, args ...string) (string, error)
}

func NewGitSHA() *GitSHA {
	return &GitSHA{runner: defaultRunner}
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
	sha, err := g.runner(ctx, "git", "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git_sha: %w", err)
	}
	if sha == "" {
		return nil, fmt.Errorf("git_sha: empty SHA from git rev-parse HEAD")
	}
	out := make(map[string]Release, len(services))
	for _, s := range services {
		out[s] = Release{Version: sha}
	}
	return out, nil
}
