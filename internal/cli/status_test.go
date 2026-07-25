package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
	"github.com/Kaikei-e/c2quay/internal/output"
)

// --- newStatusCommand: required-flag validation ---------------------------

func TestNewStatusCommand_MissingEnv_FailsBeforeAnyIO(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newStatusCommand(rt)
	cmd.SetArgs([]string{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitOperatorError, ee.Code)
	assert.Contains(t, err.Error(), "--env is required")
}

// --- renderComposeState ----------------------------------------------------

func TestRenderComposeState_NoContainers_Warns(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	renderComposeState(tw, "myapp", nil)

	assert.Contains(t, stdout.String(), "no containers for project myapp")
	assert.Contains(t, stdout.String(), "!")
}

func TestRenderComposeState_RunningHealthy_IsOk(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	renderComposeState(tw, "myapp", []composeadapter.ContainerStatus{
		{Name: "myapp-api-1", Service: "api", State: "running", Health: "healthy", Status: "Up 2 minutes"},
	})

	out := stdout.String()
	assert.Contains(t, out, "✓")
	assert.Contains(t, out, "container myapp-api-1 (service api)")
	assert.Contains(t, out, "state=running health=healthy status=Up 2 minutes")
}

// TestRenderComposeState_RunningNoHealthcheck_IsOk proves a service with no
// configured healthcheck (Health == "") is NOT treated as unhealthy — this
// is the common case for services that don't define one.
func TestRenderComposeState_RunningNoHealthcheck_IsOk(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	renderComposeState(tw, "myapp", []composeadapter.ContainerStatus{
		{Name: "myapp-web-1", Service: "web", State: "running", Health: "", Status: "Up 1 minute"},
	})

	assert.Contains(t, stdout.String(), "✓")
}

func TestRenderComposeState_RunningUnhealthy_Warns(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	renderComposeState(tw, "myapp", []composeadapter.ContainerStatus{
		{Name: "myapp-api-1", Service: "api", State: "running", Health: "unhealthy", Status: "Up 2 minutes (unhealthy)"},
	})

	out := stdout.String()
	assert.Contains(t, out, "!")
	assert.NotContains(t, out, "✓")
}

func TestRenderComposeState_Exited_Warns(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	renderComposeState(tw, "myapp", []composeadapter.ContainerStatus{
		{Name: "myapp-api-1", Service: "api", State: "exited", Status: "Exited (1) 5 seconds ago"},
	})

	out := stdout.String()
	assert.Contains(t, out, "!")
	assert.Contains(t, out, "state=exited")
}

func TestRenderComposeState_MultipleContainers_MixedStates(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	renderComposeState(tw, "myapp", []composeadapter.ContainerStatus{
		{Name: "myapp-api-1", Service: "api", State: "running", Health: "healthy"},
		{Name: "myapp-worker-1", Service: "worker", State: "exited"},
	})

	out := stdout.String()
	assert.Contains(t, out, "✓ container myapp-api-1")
	assert.Contains(t, out, "! container myapp-worker-1")
}

// --- checkBrokerEnvironment -------------------------------------------------

type fakeEnvironmentChecker struct {
	hasRelation bool
	exists      bool
	existsErr   error
}

func (f fakeEnvironmentChecker) HasRelation(rel string) bool { return f.hasRelation }
func (f fakeEnvironmentChecker) EnvironmentExists(ctx context.Context, name string) (bool, error) {
	return f.exists, f.existsErr
}

func TestCheckBrokerEnvironment_NoRelation_WarnsAndSucceeds(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	err := checkBrokerEnvironment(context.Background(), tw, fakeEnvironmentChecker{hasRelation: false}, "production")

	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "broker does not expose pb:environments")
}

func TestCheckBrokerEnvironment_Exists_Ok(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	err := checkBrokerEnvironment(context.Background(), tw, fakeEnvironmentChecker{hasRelation: true, exists: true}, "production")

	require.NoError(t, err)
	out := stdout.String()
	assert.Contains(t, out, "✓")
	assert.Contains(t, out, "production is known to the broker")
}

// TestCheckBrokerEnvironment_NotRegistered_FailsButReturnsNil preserves the
// pre-existing behaviour: an environment that is not registered with the
// broker is reported via tw.Fail, but the function itself does not error —
// only a broker-reachability problem (EnvironmentExists returning an error)
// does. Changing this would be a behaviour change, not a test addition.
func TestCheckBrokerEnvironment_NotRegistered_FailsButReturnsNil(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	err := checkBrokerEnvironment(context.Background(), tw, fakeEnvironmentChecker{hasRelation: true, exists: false}, "staging")

	require.NoError(t, err)
	out := stdout.String()
	assert.Contains(t, out, "✗")
	assert.Contains(t, out, "staging is NOT registered")
}

func TestCheckBrokerEnvironment_APIError_ReturnsError(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	tw := output.NewText(rt.stdout)
	apiErr := errors.New("broker 500")
	err := checkBrokerEnvironment(context.Background(), tw, fakeEnvironmentChecker{hasRelation: true, existsErr: apiErr}, "production")

	require.Error(t, err)
	assert.Equal(t, apiErr, err)
	assert.Contains(t, stdout.String(), "broker 500")
}
