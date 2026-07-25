package release

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// FailedStep is the broad category of what went wrong during deploy.
type FailedStep string

const (
	StepGate      FailedStep = "gate"
	StepComposeUp FailedStep = "compose-up"
	StepSmoke     FailedStep = "smoke"
	StepRecord    FailedStep = "record-deployment"
)

type RollbackHint struct {
	Env             string
	FailedAt        FailedStep
	Cause           error
	PreSnapshot     *Snapshot
	PreSnapshotFile string
	PostSnapshot    *Snapshot
	Rollback        *RollbackReport
	// RecordResults itemizes the per-service record-deployment outcome.
	// Only meaningful (and only populated by callers) when FailedAt ==
	// StepRecord: it lets Write() replace the old blanket "record-deployment
	// was NOT called" note — which is factually wrong once some services
	// succeeded — with an accurate recorded/unrecorded split.
	RecordResults []RecordResult
}

// Write emits the human-readable hint to w.
func (h RollbackHint) Write(w io.Writer) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Deployment failed. Rollback hints:")
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "Environment:    %s\n", h.Env)
	fmt.Fprintf(w, "Failed at step: %s\n", h.FailedAt)
	if h.Cause != nil {
		fmt.Fprintf(w, "Cause:          %s\n", h.Cause.Error())
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Affected services:")

	if h.PostSnapshot != nil && h.PreSnapshot != nil {
		for _, d := range Diff(h.PreSnapshot, h.PostSnapshot) {
			if d.Changed {
				fmt.Fprintf(w, "  - %s: was %s, attempted %s\n", d.Service, versionOrNone(d.Before.Version), versionOrNone(d.After.Version))
			} else {
				fmt.Fprintf(w, "  - %s: unchanged (%s)\n", d.Service, versionOrNone(d.Before.Version))
			}
		}
	} else if h.PreSnapshot != nil {
		for svc, r := range h.PreSnapshot.Releases {
			fmt.Fprintf(w, "  - %s: was %s (not yet replaced)\n", svc, versionOrNone(r.Version))
		}
	}

	fmt.Fprintln(w, "")
	if h.PreSnapshotFile != "" {
		fmt.Fprintf(w, "Pre-deploy snapshot: %s\n", h.PreSnapshotFile)
	}

	if h.Rollback != nil {
		writeRollbackSection(w, h.Rollback)
	}

	fmt.Fprintln(w, "")
	recordFailed := h.FailedAt == StepRecord && len(h.RecordResults) > 0
	if recordFailed {
		writeRecordResultsSection(w, h.RecordResults)
	} else {
		fmt.Fprintln(w, "Note: record-deployment was NOT called for the failed attempt.")
	}

	switch {
	case h.Rollback != nil && h.Rollback.Succeeded && h.Rollback.Mode != RollbackDryRun:
		fmt.Fprintln(w, "Services were restored to their pre-deploy images. The broker's")
		fmt.Fprintln(w, "view of the previous version is still correct — no broker action needed.")
	case recordFailed:
		fmt.Fprintln(w, "Fix the cause above, then re-run `c2quay deploy`. Already-recorded services")
		fmt.Fprintln(w, "are safe to record again — record-deployment is a no-op for a version that is")
		fmt.Fprintln(w, "already marked deployed to this environment.")
	default:
		fmt.Fprintln(w, "The broker still records the previous version(s). Fix or revert, then re-run `c2quay deploy`.")
	}
}

// writeRecordResultsSection prints exactly which services the broker now
// believes are deployed, which were attempted but failed, and which were
// never attempted at all (ctx cancelled before their turn), so an operator
// recovering from a partial record-deployment failure doesn't have to
// guess — and doesn't mistake "we never even tried" for "we tried and the
// broker rejected it". See RecordDeploymentError and ErrRecordNotAttempted.
func writeRecordResultsSection(w io.Writer, results []RecordResult) {
	var recorded, failed, notAttempted []RecordResult
	for _, r := range results {
		switch {
		case r.Recorded:
			recorded = append(recorded, r)
		case errors.Is(r.Err, ErrRecordNotAttempted):
			notAttempted = append(notAttempted, r)
		default:
			failed = append(failed, r)
		}
	}
	if len(notAttempted) > 0 {
		fmt.Fprintln(w, "record-deployment results (partial failure — not every service was attempted):")
	} else {
		fmt.Fprintln(w, "record-deployment results (partial failure — every service was attempted):")
	}
	if len(recorded) > 0 {
		fmt.Fprintln(w, "  Recorded (broker now shows these as deployed — no action needed):")
		for _, r := range recorded {
			fmt.Fprintf(w, "    - %s (%s@%s)\n", r.Service, r.Pacticipant, r.Version)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintln(w, "  NOT recorded (broker still shows the previous version deployed):")
		for _, r := range failed {
			fmt.Fprintf(w, "    - %s (%s@%s): %s\n", r.Service, r.Pacticipant, r.Version, errString(r.Err))
		}
	}
	if len(notAttempted) > 0 {
		fmt.Fprintln(w, "  NOT attempted (deploy was cancelled before this service's record-deployment call; broker still shows the previous version):")
		for _, r := range notAttempted {
			fmt.Fprintf(w, "    - %s (%s@%s)\n", r.Service, r.Pacticipant, r.Version)
		}
	}
}

func errString(err error) string {
	if err == nil {
		return "(unknown error)"
	}
	return err.Error()
}

func writeRollbackSection(w io.Writer, r *RollbackReport) {
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "Auto-rollback:  mode=%s", r.Mode)
	switch {
	case r.Skipped:
		fmt.Fprintf(w, " skipped (%s)\n", r.SkipReason)
		if r.ImageCaptureFailed {
			fmt.Fprintf(w, "  root cause: pre-deploy image capture failed: %s\n", r.ImageCaptureFailReason)
			fmt.Fprintln(w, "  Auto-rollback was not possible for this deploy — there was no recorded")
			fmt.Fprintln(w, "  pre-deploy image to restore. Investigate and recover manually.")
		}
		return
	case r.Mode == RollbackDryRun && r.Succeeded:
		fmt.Fprintln(w, " dry-run (no changes applied)")
	case r.Succeeded:
		fmt.Fprintf(w, " succeeded in %s\n", r.Duration.Round(time.Millisecond))
	case r.Attempted:
		fmt.Fprintf(w, " FAILED: %s\n", r.Err)
	default:
		fmt.Fprintln(w, " not attempted")
	}
	if r.Plan != nil && len(r.Plan.Services) > 0 {
		for _, svc := range r.Plan.Services {
			fmt.Fprintf(w, "  - %s ← %s\n", svc, r.Plan.Images[svc])
		}
	}
	if r.OverrideFile != "" {
		fmt.Fprintf(w, "Override file:  %s\n", r.OverrideFile)
	}
	if r.PostSnapshotFile != "" {
		fmt.Fprintf(w, "Post-rollback:  %s\n", r.PostSnapshotFile)
	}
	if r.ReportFile != "" {
		fmt.Fprintf(w, "Rollback report:%s\n", " "+r.ReportFile)
	}
}

func versionOrNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(none)"
	}
	return v
}
