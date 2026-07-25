package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
)

// RollbackMode controls whether the deploy pipeline auto-executes a rollback
// on compose-up or smoke failure.
type RollbackMode string

const (
	// RollbackOn is the default: on applicable failures, restore the
	// pre-deploy images via an override file and re-run `compose up`.
	RollbackOn RollbackMode = "on"
	// RollbackOff preserves the pre-v0.4 behaviour: hint-only, operator
	// drives the recovery by hand.
	RollbackOff RollbackMode = "off"
	// RollbackDryRun prints the plan to stderr but never invokes the
	// compose adapter.
	RollbackDryRun RollbackMode = "dry-run"
)

// ResolveRollbackMode normalises the zero value to RollbackOn so callers that
// omit the field get the documented default behaviour.
func ResolveRollbackMode(m RollbackMode) RollbackMode {
	switch m {
	case "":
		return RollbackOn
	case RollbackOn, RollbackOff, RollbackDryRun:
		return m
	default:
		return RollbackOff
	}
}

// ParseRollbackMode accepts the string form used by the CLI flag and returns
// the canonical value, or an error for anything unrecognised.
func ParseRollbackMode(s string) (RollbackMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "on", "auto", "true", "yes":
		return RollbackOn, nil
	case "off", "false", "no", "disable", "disabled":
		return RollbackOff, nil
	case "dry-run", "dryrun", "dry":
		return RollbackDryRun, nil
	default:
		return "", fmt.Errorf("invalid --auto-rollback value %q (want on|off|dry-run)", s)
	}
}

// PolicyFor reports whether auto-rollback should run for a given failed step.
//
//   - StepGate: no state changed upstream, broker view untouched → skip.
//   - StepComposeUp / StepSmoke: possibly partial Compose-side state → rollback.
//   - StepRecord: compose & smoke both succeeded; only broker bookkeeping is
//     inconsistent. Auto-rolling back here would revert a running healthy
//     deployment — operator-only territory. See ADR-0006.
func PolicyFor(step FailedStep) bool {
	switch step {
	case StepComposeUp, StepSmoke:
		return true
	default:
		return false
	}
}

// RollbackPlan is the concrete action the executor will take: services to
// re-apply and the image reference each must land on.
type RollbackPlan struct {
	Env              string            `json:"env"`
	FromSnapshotFile string            `json:"from_snapshot_file,omitempty"`
	Images           map[string]string `json:"images"`   // service → previous image ref
	Services         []string          `json:"services"` // sorted; stable output
}

// RollbackReport captures the outcome. Written to .c2quay/rollbacks/<ts>.json
// and embedded in RollbackHint for operator-visible output.
type RollbackReport struct {
	Mode             RollbackMode  `json:"mode"`
	Attempted        bool          `json:"attempted"`
	Succeeded        bool          `json:"succeeded"`
	Skipped          bool          `json:"skipped,omitempty"`
	SkipReason       string        `json:"skip_reason,omitempty"`
	Plan             *RollbackPlan `json:"plan,omitempty"`
	OverrideFile     string        `json:"override_file,omitempty"`
	PostSnapshotFile string        `json:"post_snapshot_file,omitempty"`
	StartedAt        time.Time     `json:"started_at"`
	FinishedAt       time.Time     `json:"finished_at"`
	Duration         time.Duration `json:"duration"`
	Err              string        `json:"error,omitempty"`
	ReportFile       string        `json:"report_file,omitempty"`

	// ImageCaptureFailed / ImageCaptureFailReason are copied from the
	// pre-deploy Snapshot when a rollback is skipped because that snapshot
	// couldn't capture images. Set only on a skipped report. Distinguishes
	// "rollback is genuinely unnecessary" (e.g. images already match) from
	// "rollback is IMPOSSIBLE because we don't know what to roll back to" —
	// the latter is an operational gap an operator needs to notice, not a
	// routine no-op. Per the no-silent-fallback rule this must not live
	// only in SkipReason's free text; see also Progress output emitted at
	// the point of skip.
	ImageCaptureFailed     bool   `json:"image_capture_failed,omitempty"`
	ImageCaptureFailReason string `json:"image_capture_fail_reason,omitempty"`

	// PostSnapshotImageCaptureFailed / PostSnapshotImageCaptureFailReason
	// describe the *post-rollback* snapshot's own image-capture step, taken
	// after `compose up` for the rollback has run (successfully or not).
	// Distinct from ImageCaptureFailed above, which describes the pre-deploy
	// snapshot the rollback plan was built from. A failure here does not
	// block or undo the rollback — compose has already executed — but it
	// does mean the audit trail's "where did we actually end up" record is
	// incomplete, which per the no-silent-fallback rule must be surfaced via
	// UI.Warn and logged, not just noted here.
	PostSnapshotImageCaptureFailed     bool   `json:"post_snapshot_image_capture_failed,omitempty"`
	PostSnapshotImageCaptureFailReason string `json:"post_snapshot_image_capture_fail_reason,omitempty"`
}

// BuildRollbackPlan compares pre-snapshot images against the currently-rendered
// compose config. Returns (plan, true) when rollback is meaningful, or
// (nil, false, reason) when it should be skipped.
//
// current may be nil when the caller could not render the current config; in
// that case we fall back to "restore every service recorded in pre".
func BuildRollbackPlan(pre *Snapshot, current map[string]string) (*RollbackPlan, bool, string) {
	if pre == nil {
		return nil, false, "no pre-deploy snapshot available"
	}
	if len(pre.Images) == 0 {
		return nil, false, "pre-deploy snapshot has no recorded images (fresh deploy or render failed)"
	}
	changed := map[string]string{}
	for svc, prev := range pre.Images {
		if prev == "" {
			continue
		}
		if current == nil {
			changed[svc] = prev
			continue
		}
		if cur, ok := current[svc]; !ok || cur != prev {
			changed[svc] = prev
		}
	}
	if len(changed) == 0 {
		return nil, false, "current images already match pre-deploy snapshot"
	}
	services := make([]string, 0, len(changed))
	for svc := range changed {
		services = append(services, svc)
	}
	sort.Strings(services)
	return &RollbackPlan{
		Env:      pre.Env,
		Images:   changed,
		Services: services,
	}, true, ""
}

// RollbackDeps bundles what ExecuteRollback needs. Mirrors DeployDeps in
// spirit: the CLI constructs concrete implementations; tests supply fakes.
type RollbackDeps struct {
	Compose     ComposeDeployer
	Logger      *slog.Logger
	UI          UI
	Progress    io.Writer
	SnapshotDir string // root under which snapshots & rollback artefacts are written
	WaitTimeout time.Duration
}

// ExecuteRollback drives the compensating action for a failed deploy.
//
// Flow:
//  1. Write an override YAML pinning each affected service to its previous image.
//  2. Run `docker compose up -d --wait` with the override layered on top of the
//     project's base files. We reuse the same adapter code path as deploy, so
//     the docker/compose#10596 --wait workaround still applies.
//  3. Capture a post-rollback snapshot for the audit trail.
//  4. Serialise the RollbackReport next to the snapshots.
//
// Non-fatal errors (post-snapshot capture, report write) are logged and flagged
// in the report but do not propagate: rollback's job is to restore the
// compose plane, not to guarantee perfect audit.
func ExecuteRollback(ctx context.Context, deps RollbackDeps, plan *RollbackPlan, mode RollbackMode) (*RollbackReport, error) {
	if plan == nil {
		return nil, errors.New("ExecuteRollback: plan is nil")
	}
	if err := validateRollbackDeps(deps); err != nil {
		return nil, err
	}

	report := &RollbackReport{
		Mode:      mode,
		Attempted: true,
		Plan:      plan,
		StartedAt: time.Now(),
	}
	defer func() {
		report.FinishedAt = time.Now()
		report.Duration = report.FinishedAt.Sub(report.StartedAt)
	}()

	log := deps.Logger.With(slog.String("op", "rollback"), slog.String("env", plan.Env))
	deps.UI.Step("rollback", fmt.Sprintf("%d service(s), mode=%s", len(plan.Services), mode))

	if mode == RollbackDryRun {
		deps.UI.Warn("rollback", "dry-run: no changes will be applied")
		for _, svc := range plan.Services {
			deps.UI.Warn("rollback-plan", fmt.Sprintf("%s → %s", svc, plan.Images[svc]))
		}
		report.Succeeded = true
		if path, err := writeRollbackReport(deps.SnapshotDir, report); err == nil {
			report.ReportFile = path
		}
		return report, nil
	}

	overrideDir := filepath.Join(deps.SnapshotDir, "..", "rollback")
	overrideDir = filepath.Clean(overrideDir)
	overridePath := filepath.Join(overrideDir, fmt.Sprintf("%s-override.yml", report.StartedAt.UTC().Format("20060102T150405Z")))
	if err := WriteOverrideYAML(overridePath, plan.Images); err != nil {
		report.Err = err.Error()
		deps.UI.Fail("rollback", "write override: "+err.Error())
		log.Error("write override failed", slog.String("err", err.Error()))
		return report, err
	}
	report.OverrideFile = overridePath
	log.Info("override written", slog.String("path", overridePath), slog.Int("services", len(plan.Services)))

	wait := deps.WaitTimeout
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	upErr := deps.Compose.Up(ctx, composeadapter.UpOptions{
		Services:      plan.Services,
		RemoveOrphans: false,
		Wait:          true,
		Timeout:       wait,
		ExtraFiles:    []string{overridePath},
	}, deps.Progress)
	if upErr != nil {
		report.Err = upErr.Error()
		deps.UI.Fail("rollback", "compose up: "+upErr.Error())
		log.Error("rollback compose up failed", slog.String("err", upErr.Error()))
		// Still try to capture a snapshot so operators can see where we ended.
		capturePostRollback(ctx, deps, plan.Env, report, log)
		if path, werr := writeRollbackReport(deps.SnapshotDir, report); werr == nil {
			report.ReportFile = path
		}
		return report, upErr
	}

	report.Succeeded = true
	deps.UI.Ok("rollback", fmt.Sprintf("%d service(s) restored", len(plan.Services)))
	log.Info("rollback compose up completed", slog.Int("services", len(plan.Services)))

	capturePostRollback(ctx, deps, plan.Env, report, log)
	if path, werr := writeRollbackReport(deps.SnapshotDir, report); werr == nil {
		report.ReportFile = path
	} else {
		log.Warn("rollback report write failed", slog.String("err", werr.Error()))
	}
	return report, nil
}

// capturePostRollback captures a post-rollback snapshot for the audit trail
// and writes it to disk. Failure here — either the capture itself or the
// write — is non-fatal to the rollback outcome (compose has already run by
// the time this is called), but per the project's no-silent-fallback rule it
// must never be swallowed: it is logged AND surfaced via UI.Warn, mirroring
// warnOnImageCaptureFailure in deploy.go. A snapshot whose own image-capture
// step failed (Snapshot.ImageCaptureFailed) is likewise surfaced and
// recorded on the report via PostSnapshotImageCaptureFailed — distinct from
// the pre-deploy snapshot's ImageCaptureFailed field.
func capturePostRollback(ctx context.Context, deps RollbackDeps, env string, report *RollbackReport, log *slog.Logger) {
	snap, err := CaptureSnapshot(ctx, deps.Compose, env, nil)
	if err != nil {
		msg := fmt.Sprintf("post-rollback snapshot capture failed: %v", err)
		deps.UI.Warn("rollback", msg)
		log.Warn("post-rollback snapshot capture failed", slog.String("err", err.Error()))
		return
	}
	if snap.ImageCaptureFailed {
		report.PostSnapshotImageCaptureFailed = true
		report.PostSnapshotImageCaptureFailReason = snap.ImageCaptureFailReason
		deps.UI.Warn("rollback", "post-rollback image capture failed: "+snap.ImageCaptureFailReason)
		log.Warn("post-rollback image capture failed", slog.String("reason", snap.ImageCaptureFailReason))
	}
	path, werr := snap.Write(deps.SnapshotDir, "rollback")
	if werr != nil {
		msg := fmt.Sprintf("post-rollback snapshot write failed: %v", werr)
		deps.UI.Warn("rollback", msg)
		log.Warn("post-rollback snapshot write failed", slog.String("err", werr.Error()))
		return
	}
	report.PostSnapshotFile = path
}

func writeRollbackReport(snapshotDir string, report *RollbackReport) (string, error) {
	dir := filepath.Join(snapshotDir, "..", "rollbacks")
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir rollbacks: %w", err)
	}
	name := fmt.Sprintf("%s.json", report.StartedAt.UTC().Format("20060102T150405Z"))
	path := filepath.Join(dir, name)
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// WriteOverrideYAML emits a minimal Compose override that pins
// services.<name>.image for each entry. The file is written 0600 under a
// 0750 directory.
func WriteOverrideYAML(path string, images map[string]string) error {
	if len(images) == 0 {
		return errors.New("WriteOverrideYAML: no images supplied")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir override: %w", err)
	}
	services := make([]string, 0, len(images))
	for s := range images {
		services = append(services, s)
	}
	sort.Strings(services)

	var b strings.Builder
	b.WriteString("# Generated by c2quay auto-rollback. Do not edit.\n")
	b.WriteString("services:\n")
	for _, svc := range services {
		fmt.Fprintf(&b, "  %s:\n", yamlKey(svc))
		fmt.Fprintf(&b, "    image: %s\n", yamlScalar(images[svc]))
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// yamlKey returns the service name as-is when it is a plain identifier; the
// rendered-config keys that c2quay ingests already come from `docker compose
// config --format json`, which normalises them to valid compose service keys.
func yamlKey(s string) string { return s }

// yamlScalar quotes an image reference if it contains characters that would
// confuse a plain YAML scalar. Image refs are ASCII with `:` and `@`, both
// safe bare; we still quote defensively to survive e.g. tags with `#`.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		if r == '#' || r == '&' || r == '*' || r == '!' || r == '|' || r == '>' || r == '\'' || r == '"' || r == '%' || r == '@' {
			// Wrap in double quotes and escape embedded quotes/backslashes.
			esc := strings.ReplaceAll(s, `\`, `\\`)
			esc = strings.ReplaceAll(esc, `"`, `\"`)
			return `"` + esc + `"`
		}
	}
	return s
}

func validateRollbackDeps(d RollbackDeps) error {
	if d.Compose == nil {
		return errors.New("RollbackDeps.Compose is required")
	}
	if d.Logger == nil {
		return errors.New("RollbackDeps.Logger is required")
	}
	if d.UI == nil {
		return errors.New("RollbackDeps.UI is required")
	}
	if d.Progress == nil {
		return errors.New("RollbackDeps.Progress is required")
	}
	if d.SnapshotDir == "" {
		return errors.New("RollbackDeps.SnapshotDir is required")
	}
	return nil
}

// LoadSnapshot reads a snapshot JSON file from disk. Used by the standalone
// `c2quay rollback --from-snapshot` command.
func LoadSnapshot(path string) (*Snapshot, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is an operator-supplied snapshot file.
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	return &snap, nil
}

// contextWithFreshTimeout derives a detached context for the rollback step.
// The original deploy ctx may already be cancelled (e.g., exhausted --wait
// timeout on compose up). We still honour a parent cancellation signal like
// SIGINT by watching the parent and cancelling the child when it fires.
func contextWithFreshTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.Background()
	ctx, cancel := context.WithTimeout(base, timeout)
	if parent != nil {
		go func() {
			select {
			case <-parent.Done():
				// Only propagate explicit cancellation, not deadline exhaustion
				// of the already-failed deploy ctx (the whole point of deriving
				// from Background is to survive that).
				if errors.Is(parent.Err(), context.Canceled) {
					cancel()
				}
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}
