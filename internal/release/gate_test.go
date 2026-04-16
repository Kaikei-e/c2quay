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
