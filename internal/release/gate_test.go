package release_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/broker"
	"github.com/Kaikei-e/c2quay/internal/config"
	"github.com/Kaikei-e/c2quay/internal/release"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

type fakeBrokerClient struct {
	responses map[string]*broker.CanIDeployResult
	errs      map[string]error
}

func (f *fakeBrokerClient) CanIDeploy(_ context.Context, in broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
	key := in.Pacticipant + "|" + in.Version + "|" + in.Environment
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if r, ok := f.responses[key]; ok {
		return r, nil
	}
	return nil, errors.New("unexpected input: " + key)
}

type fakeStrategy struct{ out map[string]versioning.Release }

func (f *fakeStrategy) Name() string { return "fake" }
func (f *fakeStrategy) Resolve(context.Context, []string) (map[string]versioning.Release, error) {
	return f.out, nil
}

func cfgTwoServices() *config.Config {
	return &config.Config{
		Environments: map[string]config.Environment{
			"production": {Services: map[string]config.ServiceMapping{
				"api": {Pacticipant: "api"},
				"web": {Pacticipant: "web"},
			}},
		},
	}
}

func TestBuildPlan_AllServices(t *testing.T) {
	s := &fakeStrategy{out: map[string]versioning.Release{
		"api": {Version: "v1"},
		"web": {Version: "v2"},
	}}
	plan, err := release.BuildPlan(context.Background(), cfgTwoServices(), "production", "", s)
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "web"}, plan.Services)
}

func TestBuildPlan_SingleService(t *testing.T) {
	s := &fakeStrategy{out: map[string]versioning.Release{"api": {Version: "v1"}}}
	plan, err := release.BuildPlan(context.Background(), cfgTwoServices(), "production", "api", s)
	require.NoError(t, err)
	assert.Equal(t, []string{"api"}, plan.Services)
}

func TestBuildPlan_UnknownEnvironment(t *testing.T) {
	_, err := release.BuildPlan(context.Background(), cfgTwoServices(), "staging", "", &fakeStrategy{})
	require.Error(t, err)
}

func TestGateAll_AllPass(t *testing.T) {
	tr := true
	client := &fakeBrokerClient{responses: map[string]*broker.CanIDeployResult{
		"api|v1|production": {Deployable: tr, Reason: "ok"},
		"web|v2|production": {Deployable: tr, Reason: "ok"},
	}}
	plan := &release.Plan{
		Env:      "production",
		Services: []string{"api", "web"},
		Releases: map[string]versioning.Release{
			"api": {Version: "v1"}, "web": {Version: "v2"},
		},
		Mapping: map[string]config.ServiceMapping{
			"api": {Pacticipant: "api"}, "web": {Pacticipant: "web"},
		},
	}
	outs := release.GateAll(context.Background(), client, plan)
	require.Len(t, outs, 2)
	assert.True(t, release.AllPassed(outs))
}

func TestGateAll_OneFails(t *testing.T) {
	tr, fl := true, false
	_ = fl
	client := &fakeBrokerClient{responses: map[string]*broker.CanIDeployResult{
		"api|v1|production": {Deployable: tr, Reason: "ok"},
		"web|v2|production": {Deployable: false, Reason: "contract broken"},
	}}
	plan := &release.Plan{
		Env:      "production",
		Services: []string{"api", "web"},
		Releases: map[string]versioning.Release{"api": {Version: "v1"}, "web": {Version: "v2"}},
		Mapping:  map[string]config.ServiceMapping{"api": {Pacticipant: "api"}, "web": {Pacticipant: "web"}},
	}
	outs := release.GateAll(context.Background(), client, plan)
	assert.False(t, release.AllPassed(outs))
}

// --- aggregate path ------------------------------------------------------

type fakeAggregateClient struct {
	canIDeployCalls int
	manyCalls       int
	result          *broker.CanIDeploySetResult
	err             error
}

func (f *fakeAggregateClient) CanIDeploy(_ context.Context, _ broker.CanIDeployInput) (*broker.CanIDeployResult, error) {
	f.canIDeployCalls++
	return &broker.CanIDeployResult{Deployable: true, Reason: "ok"}, nil
}

func (f *fakeAggregateClient) CanIDeployMany(_ context.Context, _ string, _ []broker.CanIDeploySelector) (*broker.CanIDeploySetResult, error) {
	f.manyCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func allOrNothingPlan() *release.Plan {
	return &release.Plan{
		Env:      "production",
		Services: []string{"acolyte", "news-creator"},
		Releases: map[string]versioning.Release{
			"acolyte":      {Version: "cd2c3499f"},
			"news-creator": {Version: "cd2c3499f"},
		},
		Mapping: map[string]config.ServiceMapping{
			"acolyte":      {Pacticipant: "acolyte"},
			"news-creator": {Pacticipant: "news-creator"},
		},
		AllOrNothing: true,
	}
}

func TestGateAll_AllOrNothing_UsesAggregate(t *testing.T) {
	tr := true
	c := &fakeAggregateClient{
		result: &broker.CanIDeploySetResult{
			Deployable: true,
			Rows: []broker.CanIDeployMatrixRow{
				{
					ConsumerName: "acolyte", ConsumerVersion: "cd2c3499f",
					ProviderName: "news-creator", ProviderVersion: "cd2c3499f",
					Verified: true, Success: tr,
				},
			},
		},
	}
	plan := allOrNothingPlan()
	outs := release.GateAll(context.Background(), c, plan)
	assert.Equal(t, 1, c.manyCalls, "aggregate call should happen exactly once")
	assert.Equal(t, 0, c.canIDeployCalls, "per-service path must not run when aggregating")
	require.Len(t, outs, 2)
	assert.True(t, release.AllPassed(outs))
}

func TestGateAll_AllOrNothing_PartialFailure(t *testing.T) {
	c := &fakeAggregateClient{
		result: &broker.CanIDeploySetResult{
			Deployable: false,
			BrokerURL:  "http://broker/matrix?...",
			Rows: []broker.CanIDeployMatrixRow{
				{
					ConsumerName: "acolyte", ConsumerVersion: "cd2c3499f",
					ProviderName: "news-creator", ProviderVersion: "cd2c3499f",
					Verified: true, Success: false,
					VerificationURL: "http://verify/bad",
				},
			},
		},
	}
	plan := allOrNothingPlan()
	outs := release.GateAll(context.Background(), c, plan)
	require.Len(t, outs, 2)
	// plan.Services order: acolyte, news-creator (alphabetical already)
	assert.Equal(t, "acolyte", outs[0].Service)
	assert.False(t, outs[0].Deployable)
	assert.Equal(t, "http://verify/bad", outs[0].VerifyURL)
	assert.Equal(t, "http://broker/matrix?...", outs[0].BrokerURL)
	assert.False(t, outs[1].Deployable)
	assert.False(t, release.AllPassed(outs))
}

func TestGateAll_AllOrNothing_SingleService_FallsBackToIndividual(t *testing.T) {
	c := &fakeAggregateClient{}
	plan := &release.Plan{
		Env:          "production",
		Services:     []string{"api"},
		Releases:     map[string]versioning.Release{"api": {Version: "v1"}},
		Mapping:      map[string]config.ServiceMapping{"api": {Pacticipant: "api"}},
		AllOrNothing: true,
	}
	outs := release.GateAll(context.Background(), c, plan)
	assert.Equal(t, 0, c.manyCalls, "single-service should skip the aggregate path")
	assert.Equal(t, 1, c.canIDeployCalls)
	assert.True(t, release.AllPassed(outs))
}

func TestGateAll_AllOrNothing_ClientLacksAggregate(t *testing.T) {
	// fakeBrokerClient implements only GateChecker; it does NOT satisfy
	// AggregateGateChecker. all_or_nothing must therefore refuse to run.
	c := &fakeBrokerClient{}
	plan := allOrNothingPlan()
	outs := release.GateAll(context.Background(), c, plan)
	require.Len(t, outs, 2)
	for _, o := range outs {
		require.Error(t, o.Err)
		assert.Contains(t, o.Err.Error(), "all_or_nothing")
	}
	assert.False(t, release.AllPassed(outs))
}

func TestGateAll_AllOrNothing_BrokerError(t *testing.T) {
	c := &fakeAggregateClient{err: errors.New("broker timeout")}
	plan := allOrNothingPlan()
	outs := release.GateAll(context.Background(), c, plan)
	require.Len(t, outs, 2)
	for _, o := range outs {
		require.Error(t, o.Err)
		assert.Contains(t, o.Err.Error(), "broker timeout")
	}
}
