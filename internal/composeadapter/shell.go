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

	"github.com/Kaikei-e/c2quay/internal/doctor"
)

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
}

type ShellAdapter struct {
	files   []string
	project string
	log     *slog.Logger
	exec    Exec
}

func NewShell(opts ShellOptions) *ShellAdapter {
	if opts.Exec == nil {
		opts.Exec = realExec{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &ShellAdapter{
		files:   opts.ComposeFiles,
		project: opts.ProjectName,
		log:     opts.Logger,
		exec:    opts.Exec,
	}
}

// baseArgs builds the prefix `-f <file> -p <project>` used by every compose call.
func (a *ShellAdapter) baseArgs() []string {
	args := []string{"compose"}
	for _, f := range a.files {
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

func (a *ShellAdapter) PsJSON(ctx context.Context) ([]ContainerStatus, error) {
	args := append(a.baseArgs(), "ps", "--all", "--format", "json")
	stdout, stderr, err := a.exec.Output(ctx, "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("docker compose ps failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return ParsePsJSON(stdout)
}

// Up runs `docker compose up -d [--wait] [--remove-orphans]`. It compensates
// for a known bug (docker/compose#10596) where `--wait` returns exit 1 even
// when every service is healthy, by cross-checking `ps` after the call.
func (a *ShellAdapter) Up(ctx context.Context, opts UpOptions, progress io.Writer) error {
	args := append(a.baseArgs(), "up", "-d")
	if opts.RemoveOrphans {
		args = append(args, "--remove-orphans")
	}
	if opts.Wait {
		args = append(args, "--wait")
		if opts.Timeout > 0 {
			args = append(args, "--wait-timeout", fmt.Sprintf("%d", int(opts.Timeout.Seconds())))
		}
	}
	args = append(args, opts.Services...)

	runErr := a.exec.RunWithStream(ctx, progress, "docker", args...)

	statuses, psErr := a.PsJSON(ctx)
	if psErr != nil {
		if runErr != nil {
			return fmt.Errorf("up failed and ps also failed: up=%w ps=%v", runErr, psErr)
		}
		return fmt.Errorf("up succeeded but ps failed: %w", psErr)
	}
	if AllServicesHealthy(statuses) {
		if runErr != nil {
			a.log.Warn("docker compose up returned non-zero but all services look healthy (docker/compose#10596)",
				slog.String("exit_error", runErr.Error()),
			)
		}
		return nil
	}
	if runErr != nil {
		return runErr
	}
	return fmt.Errorf("docker compose up exited 0 but services are not all healthy: %s", summarize(statuses))
}

func summarize(statuses []ContainerStatus) string {
	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		parts = append(parts, fmt.Sprintf("%s=%s/%s", s.Service, s.State, s.Health))
	}
	return strings.Join(parts, ",")
}
