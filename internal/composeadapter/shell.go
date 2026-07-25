package composeadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/Kaikei-e/c2quay/internal/doctor"
)

// defaultHealthRecheckDelay is the gap between the two post-failure health
// re-checks in Up. See ShellOptions.HealthRecheckDelay.
const defaultHealthRecheckDelay = 2 * time.Second

// Exec is injectable for tests. Real code uses the exec package.
type Exec interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, []byte, error)
	RunWithStream(ctx context.Context, progress io.Writer, name string, args ...string) error
}

type realExec struct{}

func (realExec) Output(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func (realExec) RunWithStream(ctx context.Context, progress io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = progress
	cmd.Stderr = progress
	return cmd.Run()
}

type ShellOptions struct {
	ComposeFiles []string
	ProjectName  string
	Logger       *slog.Logger
	Exec         Exec
	// HealthRecheckDelay is the gap between the two `ps` health re-checks
	// Up performs when `docker compose up` exits non-zero (the
	// docker/compose#10596 workaround). Defaults to 2s. Exposed mainly so
	// tests don't have to wait out the real delay.
	HealthRecheckDelay time.Duration
}

type ShellAdapter struct {
	files              []string
	project            string
	log                *slog.Logger
	exec               Exec
	healthRecheckDelay time.Duration
}

func NewShell(opts ShellOptions) *ShellAdapter {
	if opts.Exec == nil {
		opts.Exec = realExec{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HealthRecheckDelay <= 0 {
		opts.HealthRecheckDelay = defaultHealthRecheckDelay
	}
	return &ShellAdapter{
		files:              opts.ComposeFiles,
		project:            opts.ProjectName,
		log:                opts.Logger,
		exec:               opts.Exec,
		healthRecheckDelay: opts.HealthRecheckDelay,
	}
}

// baseArgs builds the prefix `-f <file> -p <project>` used by every compose call.
func (a *ShellAdapter) baseArgs() []string {
	return a.baseArgsWithExtra(nil)
}

// baseArgsWithExtra builds the prefix with additional compose files appended
// after the configured base files (same order as the CLI accepts for layering).
func (a *ShellAdapter) baseArgsWithExtra(extra []string) []string {
	args := make([]string, 0, 1+2*(len(a.files)+len(extra))+2)
	args = append(args, "compose")
	for _, f := range a.files {
		args = append(args, "-f", f)
	}
	for _, f := range extra {
		args = append(args, "-f", f)
	}
	if a.project != "" {
		args = append(args, "-p", a.project)
	}
	return args
}

func (a *ShellAdapter) Version(ctx context.Context) (VersionInfo, error) {
	stdout, stderr, err := a.exec.Output(ctx, "docker", "compose", "version", "--format", "json")
	if err != nil {
		return VersionInfo{}, fmt.Errorf("docker compose version failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if jerr := json.Unmarshal(stdout, &parsed); jerr != nil {
		// Some Compose builds still emit plain text despite --format json.
		parsed.Version = strings.TrimSpace(string(stdout))
	}
	v, perr := doctor.ParseComposeVersion(parsed.Version)
	if perr != nil {
		return VersionInfo{}, perr
	}
	info := VersionInfo{
		Raw:             parsed.Version,
		Parsed:          v,
		SupportsWait:    !v.LessThan(doctor.ComposeVersion{Major: 2, Minor: 17, Patch: 0}),
		SupportsJSONOut: !v.LessThan(doctor.ComposeVersion{Major: 2, Minor: 21, Patch: 0}),
	}
	if v.LessThan(doctor.MinComposeVersion) {
		return info, fmt.Errorf("docker compose %s is below the c2quay minimum %s (CVE-2025-62725)", v, doctor.MinComposeVersion)
	}
	return info, nil
}

func (a *ShellAdapter) Validate(ctx context.Context) error {
	args := append(a.baseArgs(), "config", "--quiet")
	_, stderr, err := a.exec.Output(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("docker compose config --quiet failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (a *ShellAdapter) RenderConfigJSON(ctx context.Context) (*RenderedConfig, error) {
	args := append(a.baseArgs(), "config", "--format", "json")
	stdout, stderr, err := a.exec.Output(ctx, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("docker compose config --format json failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	rc, err := ParseRenderedConfig(stdout)
	if err != nil {
		return nil, err
	}
	return rc, nil
}

// ConfigServices runs `docker compose config --services` and returns the
// service names, one per non-blank output line. See ADR 0013.
func (a *ShellAdapter) ConfigServices(ctx context.Context) ([]string, error) {
	args := append(a.baseArgs(), "config", "--services")
	stdout, stderr, err := a.exec.Output(ctx, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("docker compose config --services failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return parseServiceList(stdout), nil
}

func parseServiceList(raw []byte) []string {
	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

func (a *ShellAdapter) PsJSON(ctx context.Context) ([]ContainerStatus, error) {
	args := append(a.baseArgs(), "ps", "--all", "--format", "json")
	stdout, stderr, err := a.exec.Output(ctx, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("docker compose ps failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return ParsePsJSON(stdout)
}

// Pull runs `docker compose pull <services>`, streaming progress to the
// supplied writer. See ADR 0010.
func (a *ShellAdapter) Pull(ctx context.Context, services []string, progress io.Writer) error {
	args := append(a.baseArgs(), "pull")
	args = append(args, services...)
	if err := a.exec.RunWithStream(ctx, progress, "docker", args...); err != nil {
		return fmt.Errorf("docker compose pull failed: %w", err)
	}
	return nil
}

// Up runs `docker compose up -d [--wait] [--remove-orphans] [--force-recreate]`.
//
// It compensates for a known bug (docker/compose#10596) where `--wait`
// returns exit 1 even when every service is healthy. When the compose
// invocation exits non-zero, Up does not trust a single `ps` snapshot to
// override that failure: a container can look healthy for a moment and
// then crash (or vice versa), and a single check can't tell "genuinely
// fine, compose's exit code just lied" from "flaky healthcheck, real
// failure incoming." Instead it re-checks `ps` TWICE, healthRecheckDelay
// apart (2s by default), and only overrides the original exit error if
// BOTH checks agree every service is healthy. Any disagreement — a check
// that comes back unhealthy, a ps call that itself fails, or the context
// being cancelled/expiring before the second check — surfaces the original
// compose error instead of masking it.
func (a *ShellAdapter) Up(ctx context.Context, opts UpOptions, progress io.Writer) error {
	args := a.baseArgsWithExtra(opts.ExtraFiles)
	args = append(args, "up", "-d")
	if opts.RemoveOrphans {
		args = append(args, "--remove-orphans")
	}
	if opts.ForceRecreate {
		args = append(args, "--force-recreate")
		a.log.Warn("compose up invoked with --force-recreate (ADR 0011 debug escape hatch)",
			slog.Int("services", len(opts.Services)),
		)
	}
	if opts.Wait {
		args = append(args, "--wait")
		if opts.Timeout > 0 {
			args = append(args, "--wait-timeout", fmt.Sprintf("%d", int(opts.Timeout.Seconds())))
		}
	}
	args = append(args, opts.Services...)

	runErr := a.exec.RunWithStream(ctx, progress, "docker", args...)

	if runErr == nil {
		statuses, psErr := a.PsJSON(ctx)
		if psErr != nil {
			return fmt.Errorf("up succeeded but ps failed: %w", psErr)
		}
		if AllServicesHealthy(statuses) {
			return nil
		}
		return fmt.Errorf("docker compose up exited 0 but services are not all healthy: %s", summarize(statuses))
	}

	// runErr != nil from here on: known docker/compose#10596 territory.
	// (a) log at Warn with the original exit error text, unconditionally —
	// this fires whether or not the health re-checks end up overriding it,
	// so the audit trail always shows compose returned non-zero.
	a.log.Warn("docker compose up returned non-zero; re-verifying health before treating it as a failure (docker/compose#10596)",
		slog.String("exit_error", runErr.Error()),
	)

	firstStatuses, psErr := a.PsJSON(ctx)
	if psErr != nil {
		return fmt.Errorf("up failed and ps also failed: up=%w ps=%v", runErr, psErr)
	}
	if !AllServicesHealthy(firstStatuses) {
		return runErr
	}

	// (b) First check passed; wait and re-check before trusting it. A
	// transient healthy blip, or a crash a moment later, must not be
	// reported as success.
	if werr := a.waitForRecheck(ctx); werr != nil {
		// Context cancelled/expired: do not proceed to a second check, and
		// do not mask the original compose error with a context error.
		return runErr
	}

	secondStatuses, psErr2 := a.PsJSON(ctx)
	if psErr2 != nil {
		return fmt.Errorf("up failed and the second health re-check also failed: up=%w ps=%v", runErr, psErr2)
	}
	if !AllServicesHealthy(secondStatuses) {
		return fmt.Errorf("docker compose up returned non-zero and health regressed on re-check, %s later (docker/compose#10596 workaround declined): %w",
			a.healthRecheckDelay, runErr)
	}

	// Both checks agree: override the compose exit error.
	a.log.Warn("docker compose up returned non-zero but two health re-checks agree all services are healthy; overriding exit error (docker/compose#10596)",
		slog.String("exit_error", runErr.Error()),
		slog.Duration("recheck_delay", a.healthRecheckDelay),
	)
	// (c) Surface this in Progress output too, not just the audit log — an
	// operator watching `c2quay deploy` should see that compose reported a
	// failure here without having to go dig through slog output.
	fmt.Fprintf(progress,
		"note: docker compose up exited non-zero, but c2quay verified all services healthy on two checks %s apart and is proceeding (docker/compose#10596 workaround; original error: %s)\n",
		a.healthRecheckDelay, runErr.Error(),
	)
	return nil
}

// waitForRecheck blocks for healthRecheckDelay, or returns ctx.Err() early
// if the context is cancelled/expires first.
func (a *ShellAdapter) waitForRecheck(ctx context.Context) error {
	if a.healthRecheckDelay <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(a.healthRecheckDelay):
		return nil
	}
}

func summarize(statuses []ContainerStatus) string {
	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		parts = append(parts, fmt.Sprintf("%s=%s/%s", s.Service, s.State, s.Health))
	}
	return strings.Join(parts, ",")
}
