package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// MinComposeVersion is the minimum Compose CLI version c2quay supports.
// v2.40.2 fixes CVE-2025-62725 (compose include path traversal).
var MinComposeVersion = ComposeVersion{2, 40, 2}

// ComposeVersion is a parsed semver triple of a `docker compose` version string.
type ComposeVersion struct{ Major, Minor, Patch int }

func (v ComposeVersion) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

func (v ComposeVersion) LessThan(o ComposeVersion) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// Runner abstracts subprocess execution so tests can mock it.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func DefaultRunner() Runner { return execRunner{} }

// Result is the outcome of one environmental check.
type Result struct {
	Name   string
	OK     bool
	Detail string
}

// Report aggregates the results of all checks.
type Report struct {
	Results []Result
}

func (r Report) AllOK() bool {
	for _, res := range r.Results {
		if !res.OK {
			return false
		}
	}
	return true
}

// Run executes all environmental checks.
func Run(ctx context.Context, r Runner) Report {
	if r == nil {
		r = DefaultRunner()
	}
	return Report{Results: []Result{
		checkDockerDaemon(ctx, r),
		checkHyphenatedCompose(ctx, r),
		checkComposeVersion(ctx, r),
	}}
}

func checkDockerDaemon(ctx context.Context, r Runner) Result {
	out, err := r.Run(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		return Result{Name: "docker daemon", OK: false, Detail: fmt.Sprintf("docker info failed: %v", trimOutput(out))}
	}
	return Result{Name: "docker daemon", OK: true, Detail: "server " + strings.TrimSpace(string(out))}
}

func checkHyphenatedCompose(ctx context.Context, r Runner) Result {
	_, err := r.Run(ctx, "docker-compose", "version", "--short")
	if err != nil {
		return Result{Name: "docker-compose absent", OK: true, Detail: "hyphenated compose not found (good)"}
	}
	return Result{
		Name:   "docker-compose absent",
		OK:     false,
		Detail: "`docker-compose` (hyphen) detected; c2quay only supports `docker compose` (space). Remove Compose v1.",
	}
}

func checkComposeVersion(ctx context.Context, r Runner) Result {
	out, err := r.Run(ctx, "docker", "compose", "version", "--format", "json")
	if err != nil {
		return Result{Name: "docker compose version", OK: false, Detail: fmt.Sprintf("`docker compose version` failed: %v %s", err, trimOutput(out))}
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		parsed.Version = strings.TrimSpace(string(out))
	}
	v, perr := ParseComposeVersion(parsed.Version)
	if perr != nil {
		return Result{Name: "docker compose version", OK: false, Detail: perr.Error()}
	}
	if v.LessThan(MinComposeVersion) {
		return Result{
			Name:   "docker compose version",
			OK:     false,
			Detail: fmt.Sprintf("found %s, require %s or newer (CVE-2025-62725)", v, MinComposeVersion),
		}
	}
	return Result{Name: "docker compose version", OK: true, Detail: v.String()}
}

// ParseComposeVersion accepts "v2.40.2", "2.40.2", or "Docker Compose version v2.40.2".
func ParseComposeVersion(s string) (ComposeVersion, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "Docker Compose version ")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// Strip suffixes like "-desktop.1" or "+build".
	for _, sep := range []string{"-", "+"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return ComposeVersion{}, fmt.Errorf("compose version %q: unrecognized format", s)
	}
	nums := make([]int, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return ComposeVersion{}, fmt.Errorf("compose version %q: non-numeric segment %q", s, parts[i])
		}
		nums[i] = n
	}
	return ComposeVersion{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

func trimOutput(b []byte) string {
	return strings.TrimSpace(string(b))
}

// ErrFailed indicates at least one check failed (operator-fixable).
var ErrFailed = errors.New("doctor checks failed")
