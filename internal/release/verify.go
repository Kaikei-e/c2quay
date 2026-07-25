package release

import (
	"context"
	"errors"
	"fmt"

	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

// VerifyDeps bundles everything the verify orchestrator needs. The caller
// (CLI layer) constructs the concrete implementations and passes them in.
type VerifyDeps struct {
	Broker   GateChecker
	Strategy versioning.Strategy
	// Compose is OPTIONAL and defaults to nil for backward-compatible
	// construction: existing callers that only set Broker/Strategy keep
	// working exactly as before (no coverage check attempted, no notice).
	// When set, Verify opportunistically runs the same plan-time compose
	// coverage validation Deploy runs (ADR 0013) on a best-effort basis —
	// see the "Verify parity" note there. This closes the gap where
	// `c2quay verify` passed for a non-gate_only service missing from
	// Compose, only for `c2quay deploy` to hard-fail on it later.
	Compose composeServiceLister
}

// VerifyReport is the output of a verify run. The CLI formats it as text or JSON.
type VerifyReport struct {
	Plan     *Plan
	Outcomes []GateOutcome

	// CoverageChecked is true only when the compose coverage check (ADR
	// 0013 "Verify parity") actually ran to completion against a resolved
	// compose service list. False whenever no Compose dependency was wired
	// at all, or the probe to resolve that list itself failed — see
	// CoverageNotice for the latter case.
	CoverageChecked bool
	// CoverageNotice explains, for operator visibility, why compose
	// coverage was NOT checked this run — e.g. "compose coverage not
	// checked: docker: command not found". This is a deliberate, explicit
	// degradation (verify must stay usable on a broker-only box with no
	// compose CLI available), never a silent skip. Empty when
	// CoverageChecked is true, or when no Compose dependency was wired at
	// all (VerifyDeps.Compose == nil): that case is the caller's explicit
	// opt-out, not a runtime failure worth a notice.
	CoverageNotice string
	// CoverageErr is the error discovered while validating compose
	// coverage — the same shape ValidateComposeCoverage would return for
	// Deploy (e.g. `service "X" is mapped in environments.<env> but does
	// not exist in the compose config`). A non-nil CoverageErr makes
	// AllPassed() false and FirstError() return it, so `c2quay verify`
	// fails the same way a doomed `c2quay deploy` eventually would.
	CoverageErr error
}

// AllPassed is true when compose coverage (if checked) raised no error and
// every gate outcome is deployable and error-free.
func (r *VerifyReport) AllPassed() bool {
	if r.CoverageErr != nil {
		return false
	}
	return AllPassed(r.Outcomes)
}

// FirstError returns the compose coverage error (if any), else the first
// non-nil outcome error, or nil.
func (r *VerifyReport) FirstError() error {
	if r.CoverageErr != nil {
		return r.CoverageErr
	}
	for _, o := range r.Outcomes {
		if o.Err != nil {
			return o.Err
		}
	}
	return nil
}

// Verify resolves the plan and asks the broker about every service.
// It does not print anything or return non-nil error on gate failure: the
// caller inspects the report. Operator-level errors (plan build, broker
// unreachable) do return err.
//
// When deps.Compose is set, Verify also opportunistically validates compose
// coverage (ADR 0013's ValidateComposeCoverage, factored via
// checkComposeCoverage) on a best-effort basis: a genuine coverage mismatch
// is reported as a verify failure (CoverageErr), but a failure to even
// resolve the compose service list (no docker on this box, etc.) degrades
// to a loud, explicit notice (CoverageNotice) and gate checks still run —
// verify must remain usable from a broker-only box.
func Verify(ctx context.Context, cfg *config.Config, envName, onlyService string, deps VerifyDeps) (*VerifyReport, error) {
	if deps.Broker == nil {
		return nil, errors.New("VerifyDeps.Broker is required")
	}
	if deps.Strategy == nil {
		return nil, errors.New("VerifyDeps.Strategy is required")
	}
	plan, err := BuildPlan(ctx, cfg, envName, onlyService, deps.Strategy)
	if err != nil {
		return nil, fmt.Errorf("build plan: %w", err)
	}

	report := &VerifyReport{Plan: plan}
	if deps.Compose != nil {
		actual, cerr := deps.Compose.ConfigServices(ctx)
		if cerr != nil {
			report.CoverageNotice = fmt.Sprintf("compose coverage not checked: %v", cerr)
		} else {
			report.CoverageChecked = true
			report.CoverageErr = checkComposeCoverage(actual, plan, nil)
		}
	}

	report.Outcomes = GateAll(ctx, deps.Broker, plan)
	return report, nil
}
