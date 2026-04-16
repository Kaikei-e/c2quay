// Package release orchestrates the gate-check → compose-up → record pipeline.
package release

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

// BrokerClient is kept as an alias of GateChecker for backwards compatibility
// with existing tests; prefer GateChecker in new code.
type BrokerClient = GateChecker

// GateOutcome is the per-service result of a can-i-deploy check.
type GateOutcome struct {
	Service     string
	Pacticipant string
	Release     versioning.Release
	Deployable  bool
	Reason      string
	BrokerURL   string
	VerifyURL   string
	Err         error
}

// CheckService runs one gate check.
func CheckService(ctx context.Context, client GateChecker, svc, pacticipant, envName string, rel versioning.Release) GateOutcome {
	out := GateOutcome{
		Service:     svc,
		Pacticipant: pacticipant,
		Release:     rel,
	}
	res, err := client.CanIDeploy(ctx, broker.CanIDeployInput{
		Pacticipant: pacticipant,
		Version:     rel.Version,
		Environment: envName,
	})
	if err != nil {
		out.Err = err
		return out
	}
	out.Deployable = res.Deployable
	out.Reason = res.Reason
	out.BrokerURL = res.BrokerURL
	out.VerifyURL = res.VerificationURL
	return out
}

// Plan is the resolved versions for every service being gated.
type Plan struct {
	Env      string
	Services []string                         // order matters (output stability)
	Releases map[string]versioning.Release    // service -> release
	Mapping  map[string]config.ServiceMapping // service -> pacticipant mapping
}

// BuildPlan resolves versions and service-to-pacticipant mapping from config.
func BuildPlan(ctx context.Context, cfg *config.Config, envName, onlyService string, strat versioning.Strategy) (*Plan, error) {
	env, ok := cfg.LookupEnvironment(envName)
	if !ok {
		return nil, fmt.Errorf("environment %q not found in config", envName)
	}
	services := make([]string, 0, len(env.Services))
	mapping := make(map[string]config.ServiceMapping, len(env.Services))
	for name, m := range env.Services {
		if onlyService != "" && name != onlyService {
			continue
		}
		services = append(services, name)
		mapping[name] = m
	}
	if len(services) == 0 {
		if onlyService != "" {
			return nil, fmt.Errorf("service %q is not in environment %q", onlyService, envName)
		}
		return nil, errors.New("no services to check")
	}
	sort.Strings(services)

	releases, err := strat.Resolve(ctx, services)
	if err != nil {
		return nil, err
	}
	return &Plan{
		Env:      envName,
		Services: services,
		Releases: releases,
		Mapping:  mapping,
	}, nil
}
