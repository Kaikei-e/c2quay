package release

import (
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
	fmt.Fprintln(w, "Note: record-deployment was NOT called for the failed attempt.")
	if h.Rollback != nil && h.Rollback.Succeeded && h.Rollback.Mode != RollbackDryRun {
		fmt.Fprintln(w, "Services were restored to their pre-deploy images. The broker's")
		fmt.Fprintln(w, "view of the previous version is still correct — no broker action needed.")
	} else {
		fmt.Fprintln(w, "The broker still records the previous version(s). Fix or revert, then re-run `c2quay deploy`.")
	}
}

func writeRollbackSection(w io.Writer, r *RollbackReport) {
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "Auto-rollback:  mode=%s", r.Mode)
	switch {
	case r.Skipped:
		fmt.Fprintf(w, " skipped (%s)\n", r.SkipReason)
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
