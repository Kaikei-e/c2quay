package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// composeServiceLister is the minimal Compose surface plan-time coverage
// validation needs: the actual service names Compose would recognise for
// this project. Defined here (consumer-side) rather than depending on the
// whole ComposeDeployer interface.
type composeServiceLister interface {
	ConfigServices(ctx context.Context) ([]string, error)
}

// ValidateComposeCoverage checks, before the Pact gate runs, that every
// service in plan which is NOT marked gate_only actually exists in the
// resolved Compose config. It also warns (does not fail) when a gate_only
// service unexpectedly DOES exist in Compose — that is probably a
// misconfiguration (the mapping was never flipped back after the service
// moved into compose.yaml), but not by itself a reason to block a deploy.
//
// This is the fix for the "whole-batch `no such service` hard failure
// after the gate has already passed" incident class: docker compose
// validates its positional service arguments before starting anything, so
// a single stale/missing mapping used to fail the ENTIRE deploy after every
// other service had already cleared can-i-deploy. See ADR 0013.
//
// Resolving the Compose service list shells out to `docker compose config
// --services`. If that call itself fails, the default behaviour is a hard
// error — c2quay does not silently skip validation just because Compose is
// unreachable. The one exception is --dry-run: an operator planning from a
// box without a live Compose/Docker installation should still be able to
// see the gate check, so a ConfigServices failure in dry-run is downgraded
// to a loud UI warning and validation is skipped for that run.
func ValidateComposeCoverage(ctx context.Context, compose composeServiceLister, plan *Plan, dryRun bool, ui UI) error {
	if plan == nil {
		return errors.New("ValidateComposeCoverage: plan is nil")
	}
	actual, err := compose.ConfigServices(ctx)
	if err != nil {
		if dryRun {
			if ui != nil {
				ui.Warn("compose-coverage", fmt.Sprintf(
					"dry-run: could not resolve compose service list (%s); skipping gate_only coverage validation", err))
			}
			return nil
		}
		return fmt.Errorf("resolve compose service list: %w", err)
	}
	return checkComposeCoverage(actual, plan, ui)
}

// checkComposeCoverage holds the actual missing/misconfigured-gate_only
// comparison, factored out of ValidateComposeCoverage so `c2quay verify`
// (internal/release/verify.go) can run the identical check on a
// best-effort basis without inheriting Deploy's hard-fail-outside-dry-run
// behaviour on ConfigServices errors — see the "Verify parity" note on
// ADR 0013.
func checkComposeCoverage(actual []string, plan *Plan, ui UI) error {
	actualSet := make(map[string]struct{}, len(actual))
	for _, s := range actual {
		actualSet[s] = struct{}{}
	}

	var missing []string
	for _, name := range plan.Services {
		mapping := plan.Mapping[name]
		_, exists := actualSet[name]
		switch {
		case !mapping.GateOnly && !exists:
			missing = append(missing, name)
		case mapping.GateOnly && exists:
			if ui != nil {
				ui.Warn("compose-coverage", fmt.Sprintf(
					"%q is mapped in environments.%s with gate_only: true, but it DOES exist in the compose config; "+
						"this is probably a misconfiguration (was the mapping not flipped back after the service moved into compose.yaml?)",
					name, plan.Env))
			}
		}
	}

	if len(missing) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(missing))
	for _, name := range missing {
		msgs = append(msgs, fmt.Sprintf(
			"service %q is mapped in environments.%s but does not exist in the compose config; "+
				"if it is deployed outside compose, mark it gate_only: true",
			name, plan.Env))
	}
	return errors.New(strings.Join(msgs, "; "))
}
