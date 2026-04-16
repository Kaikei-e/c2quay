package release

import (
	"fmt"
	"io"
	"strings"
)

// FailedStep is the broad category of what went wrong during deploy.
type FailedStep string

const (
	StepGate     FailedStep = "gate"
	StepComposeUp FailedStep = "compose-up"
	StepSmoke    FailedStep = "smoke"
	StepRecord   FailedStep = "record-deployment"
)

type RollbackHint struct {
	Env           string
	FailedAt      FailedStep
	Cause         error
	PreSnapshot   *Snapshot
	PreSnapshotFile string
	PostSnapshot  *Snapshot
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

	if h.PostSnapshot != nil {
		for _, d := range Diff(h.PreSnapshot, h.PostSnapshot) {
			if d.Changed {
				fmt.Fprintf(w, "  - %s: was %s, attempted %s\n", d.Service, versionOrNone(d.Before.Version), versionOrNone(d.After.Version))
			} else {
				fmt.Fprintf(w, "  - %s: unchanged (%s)\n", d.Service, versionOrNone(d.Before.Version))
			}
		}
	} else {
		for svc, r := range h.PreSnapshot.Releases {
			fmt.Fprintf(w, "  - %s: was %s (not yet replaced)\n", svc, versionOrNone(r.Version))
		}
	}

	fmt.Fprintln(w, "")
	if h.PreSnapshotFile != "" {
		fmt.Fprintf(w, "Pre-deploy snapshot: %s\n", h.PreSnapshotFile)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Note: record-deployment was NOT called for the failed attempt.")
	fmt.Fprintln(w, "The broker still records the previous version(s). Fix or revert, then re-run `c2quay deploy`.")
}

func versionOrNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(none)"
	}
	return v
}
