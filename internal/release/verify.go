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
}

// VerifyReport is the output of a verify run. The CLI formats it as text or JSON.
type VerifyReport struct {
	Plan     *Plan
	Outcomes []GateOutcome
}

// AllPassed is true when every outcome is deployable and error-free.
func (r *VerifyReport) AllPassed() bool { return AllPassed(r.Outcomes) }

// FirstError returns the first non-nil outcome error, or nil.
func (r *VerifyReport) FirstError() error {
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
	outcomes := GateAll(ctx, deps.Broker, plan)
	return &VerifyReport{Plan: plan, Outcomes: outcomes}, nil
}
