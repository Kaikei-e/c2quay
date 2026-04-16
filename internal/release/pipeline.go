package release

import (
	"context"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

// ParallelismDefault controls the service fan-out for gate checks.
const ParallelismDefault = 4

// GateAll runs gate checks for every service in plan concurrently.
// The outcome slice is returned in plan.Services order regardless of
// completion order so output is deterministic.
func GateAll(ctx context.Context, client GateChecker, plan *Plan) []GateOutcome {
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
