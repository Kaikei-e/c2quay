package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

// DeployDeps collects the collaborators Deploy needs. The CLI layer builds
// these from config; tests can substitute fakes.
type DeployDeps struct {
	Broker      DeployBroker
	Compose     ComposeDeployer
	Strategy    versioning.Strategy
	Logger      *slog.Logger
	UI          UI
	Progress    io.Writer
	Stderr      io.Writer
	SnapshotDir string
	// RollbackMode controls auto-rollback on compose-up / smoke failures.
	// Zero value defaults to RollbackOn (opt-out). See ADR-0006.
	RollbackMode RollbackMode
	// RollbackTimeout bounds the rollback's own `compose up --wait`. Defaults
	// to 2× Deploy.WaitTimeout, or 3 minutes if neither is set.
	RollbackTimeout time.Duration
	// ForceRecreate passes `--force-recreate` through to compose up. Debug
	// escape hatch for the fresh-build-same-tag case. See ADR 0011.
	ForceRecreate bool
}

// DeployReport captures what happened, successful or not. On failure the
// caller uses FailedAtStep to decide the exit code and whether to emit a
// rollback hint.
type DeployReport struct {
	Plan             *Plan
	Outcomes         []GateOutcome
	Pre              *Snapshot
	Post             *Snapshot
	PreSnapshotFile  string
	PostSnapshotFile string
	FailedAtStep     FailedStep
	FailedCause      error
	Rollback         *RollbackReport
	// RecordResults itemizes the outcome of the record-deployment step,
	// one entry per plan.Services entry, in plan order. Populated whenever
	// step (5) runs at all — even on partial failure, so callers can tell
	// exactly which services the broker now believes are deployed and which
	// still show the previous version. See RecordDeploymentError.
	RecordResults []RecordResult
	StartedAt     time.Time
	FinishedAt    time.Time
}

// RecordResult is the per-service outcome of a record-deployment POST.
type RecordResult struct {
	Service     string
	Pacticipant string
	Version     string
	Recorded    bool
	// Err is nil when Recorded is true. When Recorded is false, Err is
	// either the broker's failure for an attempted call, or
	// ErrRecordNotAttempted (wrapped) when the call was never issued at
	// all — e.g. because ctx was already cancelled before this service's
	// turn. Callers that need to tell these apart (rollback-hint rendering,
	// in particular) should check errors.Is(r.Err, ErrRecordNotAttempted)
	// rather than treating every unrecorded service as "attempted and
	// failed".
	Err error
}

// ErrRecordNotAttempted marks a RecordResult whose record-deployment call
// was never issued in the first place, distinct from one that was issued
// and failed. recordAllDeployments checks ctx.Err() before every iteration;
// once the context is cancelled it stops POSTing to the broker and marks
// every remaining service this way instead of looping through calls that
// are doomed to fail. See M2 / the ctx-cancellation reliability fix.
var ErrRecordNotAttempted = errors.New("record-deployment: not attempted (context cancelled before this service's turn)")

// RecordDeploymentError is returned by Deploy when at least one service's
// record-deployment call failed. Unlike the previous stop-at-first-error
// behaviour, every service in the plan is always attempted first — this
// error itemizes exactly which services were recorded (the broker's view is
// now correct for them) and which were not (the broker still shows their
// previous version), so an operator doesn't have to guess or re-derive that
// split from logs before deciding how to recover.
type RecordDeploymentError struct {
	Results []RecordResult
}

func (e *RecordDeploymentError) Error() string {
	var recorded, failed []string
	for _, r := range e.Results {
		if r.Recorded {
			recorded = append(recorded, r.Service)
		} else {
			failed = append(failed, fmt.Sprintf("%s: %v", r.Service, r.Err))
		}
	}
	return fmt.Sprintf("record-deployment: %d/%d services recorded successfully; recorded=%v failed=%v",
		len(recorded), len(e.Results), recorded, failed)
}

// Unwrap exposes every per-service failure so errors.Is/As can still match
// through this aggregate (e.g. broker.ErrUnexpectedStatus from one of the
// failed calls).
func (e *RecordDeploymentError) Unwrap() []error {
	errs := make([]error, 0, len(e.Results))
	for _, r := range e.Results {
		if r.Err != nil {
			errs = append(errs, r.Err)
		}
	}
	return errs
}

// Recorded returns the services that were successfully recorded.
func (e *RecordDeploymentError) Recorded() []string {
	var out []string
	for _, r := range e.Results {
		if r.Recorded {
			out = append(out, r.Service)
		}
	}
	return out
}

// Unrecorded returns the services that were NOT recorded — this includes
// both attempted-and-failed and not-attempted services. Use NotAttempted to
// separate the two.
func (e *RecordDeploymentError) Unrecorded() []string {
	var out []string
	for _, r := range e.Results {
		if !r.Recorded {
			out = append(out, r.Service)
		}
	}
	return out
}

// NotAttempted returns the services whose record-deployment call was never
// issued at all (ctx was already cancelled by the time their turn came up),
// as distinct from a service whose call was issued and failed. See
// ErrRecordNotAttempted.
func (e *RecordDeploymentError) NotAttempted() []string {
	var out []string
	for _, r := range e.Results {
		if !r.Recorded && errors.Is(r.Err, ErrRecordNotAttempted) {
			out = append(out, r.Service)
		}
	}
	return out
}

// Deploy runs the full lock-held pipeline. The caller MUST hold an
// environment lock for the duration of this call. See internal/lock.
//
// Ordering is strict:
//
//	(1) pre-snapshot
//	(2) gate check
//	(3) compose up
//	(4) smoke (optional)
//	(5) record-deployment         ← always last
//	(6) post-snapshot
//
// record-deployment is never called before step 5. A failure in (1)-(4)
// leaves the broker's view unchanged, which is what ADR 0004 requires.
func Deploy(ctx context.Context, cfg *config.Config, envName, onlyService string, dryRun bool, deps DeployDeps) (*DeployReport, error) {
	if err := validateDeployDeps(deps); err != nil {
		return nil, err
	}
	log := deps.Logger.With(slog.String("cmd", "deploy"), slog.String("env", envName))
	report := &DeployReport{StartedAt: time.Now()}
	defer func() { report.FinishedAt = time.Now() }()

	// (1) build plan + pre-snapshot
	plan, err := BuildPlan(ctx, cfg, envName, onlyService, deps.Strategy)
	if err != nil {
		return report, fmt.Errorf("build plan: %w", err)
	}
	report.Plan = plan
	deps.UI.Step("plan", fmt.Sprintf("%d service(s)", len(plan.Services)))

	// (1a) plan-time compose coverage validation — fail fast, BEFORE the
	// gate runs. See ADR 0013: a mapped service missing from Compose (and
	// not marked gate_only) must never reach `docker compose up`, where it
	// would hard-fail the ENTIRE deploy after every other service already
	// cleared the Pact gate.
	if err := ValidateComposeCoverage(ctx, deps.Compose, plan, dryRun, deps.UI); err != nil {
		return report, fmt.Errorf("compose coverage: %w", err)
	}

	pre, err := CaptureSnapshot(ctx, deps.Compose, envName, map[string]versioning.Release{})
	if err != nil {
		return report, fmt.Errorf("pre snapshot: %w", err)
	}
	preFile, err := pre.Write(deps.SnapshotDir, "pre")
	if err != nil {
		return report, fmt.Errorf("write pre snapshot: %w", err)
	}
	report.Pre = pre
	report.PreSnapshotFile = preFile
	deps.UI.Ok("pre-snapshot", preFile)
	log.Info("step completed", slog.String("step", "pre-snapshot"), slog.String("file", preFile))
	warnOnImageCaptureFailure(deps, log, "pre-snapshot", pre)

	// (2) gate
	gateStart := time.Now()
	outcomes := GateAll(ctx, deps.Broker, plan)
	report.Outcomes = outcomes

	passed := 0
	for _, o := range outcomes {
		if o.Err == nil && o.Deployable {
			passed++
		}
	}
	log.Info("gate check completed",
		slog.GroupAttrs("gate",
			slog.Int("services_checked", len(outcomes)),
			slog.Int("services_passed", passed),
			slog.Duration("duration", time.Since(gateStart)),
		),
		slog.GroupAttrs("broker",
			slog.String("url", cfg.Broker.BaseURL),
			slog.Int("api_calls", deps.Broker.APICallCount()),
		),
	)
	for _, o := range outcomes {
		switch {
		case o.Err != nil:
			deps.UI.Fail("can-i-deploy: "+o.Pacticipant, o.Err.Error())
			return failReport(report, StepGate, o.Err)
		case !o.Deployable:
			deps.UI.Fail("can-i-deploy: "+o.Pacticipant, o.Reason)
			return failReport(report, StepGate, fmt.Errorf("%w: %s", broker.ErrGateFailed, o.Reason))
		default:
			deps.UI.Ok("can-i-deploy: "+o.Pacticipant, o.Release.Version)
		}
	}

	if dryRun {
		deps.UI.Warn("dry-run", "skipping docker compose up")
		return report, nil
	}

	// (2a)+(3) optional pull + compose up. gate_only services (ADR 0013)
	// never reach either call: they are excluded from plan.DeployServices.
	// If DeployServices is empty (every service in scope is gate_only),
	// we skip compose entirely rather than call Up with a zero-length
	// service list, which Compose would interpret as "every service in
	// the project" — the opposite of what an empty deploy target means
	// here.
	if len(plan.DeployServices) == 0 {
		deps.UI.Warn("compose up", "skipped: no deploy-target services in plan (all mapped services are gate_only)")
		log.Info("compose up skipped", slog.String("reason", "all mapped services are gate_only"))
	} else {
		// (2a) optional pull — see ADR 0010. Runs only when the operator has
		// opted in via `deploy.pull: always`; `missing` is handled natively by
		// Compose during up, so we skip it here.
		if cfg.Deploy.Pull == "always" {
			pullStart := time.Now()
			if err := deps.Compose.Pull(ctx, plan.DeployServices, deps.Progress); err != nil {
				deps.UI.Fail("compose pull", err.Error())
				return failReport(report, StepComposeUp, err)
			}
			deps.UI.Ok("compose pull", fmt.Sprintf("%d service(s)", len(plan.DeployServices)))
			log.Info("compose pull completed",
				slog.GroupAttrs("compose",
					slog.String("policy", cfg.Deploy.Pull),
					slog.Int("services", len(plan.DeployServices)),
					slog.Duration("duration", time.Since(pullStart)),
				),
			)
		}

		// (3) compose up — known --wait bug is handled by the adapter.
		composeStart := time.Now()
		upErr := deps.Compose.Up(ctx, composeadapter.UpOptions{
			Services:      plan.DeployServices,
			RemoveOrphans: true,
			Wait:          cfg.Deploy.Wait || cfg.Deploy.WaitTimeout > 0,
			Timeout:       cfg.Deploy.WaitTimeout,
			ForceRecreate: deps.ForceRecreate,
		}, deps.Progress)
		if upErr != nil {
			deps.UI.Fail("compose up", upErr.Error())
			report.Post, _ = CaptureSnapshot(ctx, deps.Compose, envName, plan.Releases)
			warnOnImageCaptureFailure(deps, log, "post-snapshot", report.Post)
			maybeRollback(ctx, deps, cfg, report, StepComposeUp)
			return failReport(report, StepComposeUp, upErr)
		}
		deps.UI.Ok("compose up", fmt.Sprintf("%d service(s) running", len(plan.DeployServices)))
		log.Info("compose up completed",
			slog.GroupAttrs("compose",
				slog.Int("services", len(plan.DeployServices)),
				slog.Duration("duration", time.Since(composeStart)),
			),
		)
	}

	// (4) smoke
	if cfg.Deploy.Smoke.Command != "" {
		if err := RunSmoke(ctx, cfg.Deploy.Smoke, envName, deps.Progress); err != nil {
			deps.UI.Fail("smoke", err.Error())
			report.Post, _ = CaptureSnapshot(ctx, deps.Compose, envName, plan.Releases)
			warnOnImageCaptureFailure(deps, log, "post-snapshot", report.Post)
			maybeRollback(ctx, deps, cfg, report, StepSmoke)
			return failReport(report, StepSmoke, err)
		}
		deps.UI.Ok("smoke", "passed")
	}

	// (5) record-deployment — per ADR 0004, always last.
	//
	// Every service in the plan is attempted, even after an earlier one
	// fails: stopping at the first error would leave the broker holding a
	// mix of "recorded" and "not recorded" services with no record of which
	// is which, which is exactly the inconsistency this itemization exists
	// to prevent. See RecordDeploymentError.
	recordStart := time.Now()
	results := recordAllDeployments(ctx, deps, cfg, envName, plan)
	report.RecordResults = results
	if recErr := recordDeploymentError(results); recErr != nil {
		deps.UI.Fail("record-deployment", recErr.Error())
		report.Post, _ = CaptureSnapshot(ctx, deps.Compose, envName, plan.Releases)
		return failReport(report, StepRecord, recErr)
	}
	deps.UI.Ok("record-deployment", fmt.Sprintf("%d recorded in %s", len(plan.Services), time.Since(recordStart).Round(time.Millisecond)))

	// (6) post-snapshot
	post, err := CaptureSnapshot(ctx, deps.Compose, envName, plan.Releases)
	if err != nil {
		log.Warn("post-snapshot failed (deploy itself succeeded)", slog.String("err", err.Error()))
	} else {
		report.Post = post
		warnOnImageCaptureFailure(deps, log, "post-snapshot", post)
		if pf, werr := post.Write(deps.SnapshotDir, "post"); werr != nil {
			log.Warn("post-snapshot write failed", slog.String("err", werr.Error()))
		} else {
			report.PostSnapshotFile = pf
			deps.UI.Ok("post-snapshot", pf)
		}
	}
	log.Info("deploy completed",
		slog.Duration("total_duration", time.Since(report.StartedAt)),
		slog.Int("services", len(plan.Services)),
	)
	return report, nil
}

// recordAllDeployments POSTs record-deployment for every service in the
// plan, unconditionally attempting all of them regardless of earlier
// failures, and returns one RecordResult per service in plan order.
//
// Before each POST it checks ctx.Err(): once the context is cancelled (e.g.
// operator Ctrl-C, or an upstream deadline), further broker calls are
// doomed to fail anyway, so every remaining service is marked "not
// attempted" (ErrRecordNotAttempted) instead of looping through failing
// POSTs. This is distinct from "attempted and failed" — see RecordResult.
func recordAllDeployments(ctx context.Context, deps DeployDeps, cfg *config.Config, envName string, plan *Plan) []RecordResult {
	results := make([]RecordResult, 0, len(plan.Services))
	for _, svc := range plan.Services {
		mapping := cfg.Environments[envName].Services[svc]
		rel := plan.Releases[svc]

		if cerr := ctx.Err(); cerr != nil {
			notAttemptedErr := fmt.Errorf("%w: %v", ErrRecordNotAttempted, cerr)
			results = append(results, RecordResult{
				Service:     svc,
				Pacticipant: mapping.Pacticipant,
				Version:     rel.Version,
				Recorded:    false,
				Err:         notAttemptedErr,
			})
			deps.UI.Warn("record-deployment: "+mapping.Pacticipant, "not attempted: cancelled ("+cerr.Error()+")")
			continue
		}

		err := deps.Broker.RecordDeployment(ctx, broker.RecordDeploymentInput{
			Pacticipant: mapping.Pacticipant,
			Version:     rel.Version,
			Environment: envName,
		})
		results = append(results, RecordResult{
			Service:     svc,
			Pacticipant: mapping.Pacticipant,
			Version:     rel.Version,
			Recorded:    err == nil,
			Err:         err,
		})
		if err != nil {
			deps.UI.Fail("record-deployment: "+mapping.Pacticipant, err.Error())
		} else {
			deps.UI.Ok("record-deployment: "+mapping.Pacticipant, rel.Version)
		}
	}
	return results
}

// recordDeploymentError returns nil when every result recorded
// successfully, or a *RecordDeploymentError itemizing the split otherwise.
func recordDeploymentError(results []RecordResult) error {
	for _, r := range results {
		if !r.Recorded {
			return &RecordDeploymentError{Results: results}
		}
	}
	return nil
}

// ComposeFilesHelper makes "snapshot dir with env name in it" conveniently.
func DefaultSnapshotDir() string { return filepath.Join(".c2quay", "snapshots") }

func validateDeployDeps(d DeployDeps) error {
	if d.Broker == nil {
		return errors.New("DeployDeps.Broker is required")
	}
	if d.Compose == nil {
		return errors.New("DeployDeps.Compose is required")
	}
	if d.Strategy == nil {
		return errors.New("DeployDeps.Strategy is required")
	}
	if d.UI == nil {
		return errors.New("DeployDeps.UI is required")
	}
	if d.Logger == nil {
		return errors.New("DeployDeps.Logger is required")
	}
	if d.Progress == nil {
		return errors.New("DeployDeps.Progress is required")
	}
	if d.SnapshotDir == "" {
		return errors.New("DeployDeps.SnapshotDir is required")
	}
	return nil
}

func failReport(r *DeployReport, step FailedStep, cause error) (*DeployReport, error) {
	r.FailedAtStep = step
	r.FailedCause = cause
	return r, cause
}

// warnOnImageCaptureFailure surfaces a snapshot's image-capture failure
// loudly — both to the operator-visible UI (which routes to Progress/stdout
// depending on the caller) and to the audit log — instead of letting it sit
// silently until a later auto-rollback quietly no-ops. Per the project's
// no-silent-fallback rule, "rollback will not be possible" must never be a
// slog-only fact.
func warnOnImageCaptureFailure(deps DeployDeps, log *slog.Logger, label string, snap *Snapshot) {
	if snap == nil || !snap.ImageCaptureFailed {
		return
	}
	msg := fmt.Sprintf("rollback will not be possible for this deploy: %s", snap.ImageCaptureFailReason)
	deps.UI.Warn(label, msg)
	log.Warn("image capture failed during snapshot; rollback will be unavailable",
		slog.String("snapshot", label),
		slog.String("reason", snap.ImageCaptureFailReason),
	)
}

// maybeRollback runs the auto-rollback flow when policy allows and the mode
// is not Off. Outcome (success, failure, skipped) is recorded on
// report.Rollback; errors never shadow the original deploy failure.
func maybeRollback(parent context.Context, deps DeployDeps, cfg *config.Config, report *DeployReport, step FailedStep) {
	mode := ResolveRollbackMode(deps.RollbackMode)
	if mode == RollbackOff {
		return
	}
	if !PolicyFor(step) {
		return
	}
	if report.Pre == nil {
		reason := "no pre-deploy snapshot available"
		deps.UI.Warn("rollback", "skipped: "+reason)
		report.Rollback = &RollbackReport{
			Mode:       mode,
			Skipped:    true,
			SkipReason: reason,
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		}
		return
	}
	currentImages := map[string]string{}
	if report.Post != nil && len(report.Post.Images) > 0 {
		currentImages = report.Post.Images
	}
	plan, ok, reason := BuildRollbackPlan(report.Pre, currentImages)
	if !ok {
		// Loud on skip: printed to the operator-visible UI (not just slog),
		// and — when the root cause is a failed image capture rather than a
		// benign "already up to date" — flagged explicitly in the persisted
		// RollbackReport so it's visible without cross-referencing the
		// pre-deploy snapshot file.
		deps.UI.Warn("rollback", "skipped: "+reason)
		rep := &RollbackReport{
			Mode:       mode,
			Skipped:    true,
			SkipReason: reason,
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		}
		if report.Pre.ImageCaptureFailed {
			rep.ImageCaptureFailed = true
			rep.ImageCaptureFailReason = report.Pre.ImageCaptureFailReason
			deps.UI.Warn("rollback", "rollback is not possible for this deploy — pre-deploy image capture failed: "+report.Pre.ImageCaptureFailReason)
		}
		report.Rollback = rep
		return
	}
	plan.FromSnapshotFile = report.PreSnapshotFile

	timeout := deps.RollbackTimeout
	if timeout <= 0 {
		if cfg != nil && cfg.Deploy.WaitTimeout > 0 {
			timeout = 2 * cfg.Deploy.WaitTimeout
		} else {
			timeout = 3 * time.Minute
		}
	}
	ctx, cancel := contextWithFreshTimeout(parent, timeout)
	defer cancel()

	rdeps := RollbackDeps{
		Compose:     deps.Compose,
		Logger:      deps.Logger,
		UI:          deps.UI,
		Progress:    deps.Progress,
		SnapshotDir: deps.SnapshotDir,
		WaitTimeout: timeout,
	}
	rep, rerr := ExecuteRollback(ctx, rdeps, plan, mode)
	report.Rollback = rep
	if rerr != nil {
		// Surface, but never overwrite the original cause.
		deps.Logger.Error("auto-rollback did not complete cleanly",
			slog.String("err", rerr.Error()),
			slog.String("triggered_by", string(step)),
		)
	}
}
