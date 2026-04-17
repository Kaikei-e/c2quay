package release

import (
	"context"
	"errors"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/Kaikei-e/c2quay/internal/broker"
)

// ParallelismDefault controls the service fan-out for gate checks.
const ParallelismDefault = 4

// GateAll runs gate checks for every service in plan.
//
// Two shapes are supported:
//
//   - Per-service fan-out (default): each service is checked independently
//     against the broker. This is correct when deployments are independent
//     and matches the behaviour of the legacy can-i-deploy endpoint.
//   - Aggregate matrix query (all_or_nothing, 2+ services): the candidate
//     set is sent in a single matrix request so the broker evaluates the
//     services together. This is the fix for monolithic rollouts where
//     every candidate is deployed with the same version: per-service
//     queries falsely assume "everything else stays on current prod" and
//     gate the rollout on verifications that are already known to pass
//     within the candidate set.
//
// Single-service plans always use the per-service path even with
// AllOrNothing=true, because the aggregate has no advantage for one
// selector and it keeps `--service foo` usable for probing.
//
// The outcome slice is returned in plan.Services order regardless of
// completion order so output is deterministic.
func GateAll(ctx context.Context, client GateChecker, plan *Plan) []GateOutcome {
	if plan.AllOrNothing && len(plan.Services) > 1 {
		agg, ok := client.(AggregateGateChecker)
		if !ok {
			return gateAllError(plan, errors.New("all_or_nothing requires a broker client that supports aggregate can-i-deploy"))
		}
		return gateAggregate(ctx, agg, plan)
	}
	return gateIndividual(ctx, client, plan)
}

// gateIndividual is the per-service fan-out path — unchanged from the
// pre-aggregate implementation.
func gateIndividual(ctx context.Context, client GateChecker, plan *Plan) []GateOutcome {
	results := make(map[string]GateOutcome, len(plan.Services))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(ParallelismDefault)

	for _, svc := range plan.Services {
		g.Go(func() error {
			mapping := plan.Mapping[svc]
			release := plan.Releases[svc]
			out := CheckService(gctx, client, svc, mapping.Pacticipant, plan.Env, release)
			mu.Lock()
			results[svc] = out
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()

	return orderedOutcomes(plan, results)
}

// gateAggregate collapses the fan-out into one matrix query. The broker's
// summary tells us whether the set is deployable as a group, and the matrix
// rows let us attribute failure to specific pacticipants so the per-service
// output stays meaningful.
func gateAggregate(ctx context.Context, client AggregateGateChecker, plan *Plan) []GateOutcome {
	selectors := make([]broker.CanIDeploySelector, 0, len(plan.Services))
	for _, svc := range plan.Services {
		selectors = append(selectors, broker.CanIDeploySelector{
			Pacticipant: plan.Mapping[svc].Pacticipant,
			Version:     plan.Releases[svc].Version,
		})
	}

	res, err := client.CanIDeployMany(ctx, plan.Env, selectors)
	if err != nil {
		return gateAllError(plan, err)
	}
	verdicts := broker.PerPacticipantVerdicts(res, selectors)

	outcomes := make([]GateOutcome, 0, len(plan.Services))
	for _, svc := range plan.Services {
		mapping := plan.Mapping[svc]
		rel := plan.Releases[svc]
		v := verdicts[mapping.Pacticipant]
		outcomes = append(outcomes, GateOutcome{
			Service:     svc,
			Pacticipant: mapping.Pacticipant,
			Release:     rel,
			Deployable:  v.Deployable,
			Reason:      v.Reason,
			BrokerURL:   res.BrokerURL,
			VerifyURL:   v.VerifyURL,
		})
	}
	return outcomes
}

// gateAllError produces one error outcome per service, used when the whole
// aggregate request fails in a way that blocks every service uniformly
// (e.g. broker unreachable, relation missing, client does not support
// aggregate).
func gateAllError(plan *Plan, err error) []GateOutcome {
	outcomes := make([]GateOutcome, 0, len(plan.Services))
	for _, svc := range plan.Services {
		mapping := plan.Mapping[svc]
		rel := plan.Releases[svc]
		outcomes = append(outcomes, GateOutcome{
			Service:     svc,
			Pacticipant: mapping.Pacticipant,
			Release:     rel,
			Err:         err,
		})
	}
	return outcomes
}

func orderedOutcomes(plan *Plan, results map[string]GateOutcome) []GateOutcome {
	ordered := make([]GateOutcome, 0, len(plan.Services))
	keys := make([]string, 0, len(plan.Services))
	keys = append(keys, plan.Services...)
	sort.Strings(keys)
	for _, k := range keys {
		ordered = append(ordered, results[k])
	}
	return ordered
}

// AllPassed reports whether every outcome was both error-free and deployable.
func AllPassed(outcomes []GateOutcome) bool {
	for _, o := range outcomes {
		if o.Err != nil || !o.Deployable {
			return false
		}
	}
	return true
}
