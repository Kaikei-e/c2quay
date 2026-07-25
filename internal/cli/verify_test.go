package cli

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/output"
	"github.com/Kaikei-e/c2quay/internal/release"
	"github.com/Kaikei-e/c2quay/internal/versioning"
)

func samplePlan() *release.Plan {
	return &release.Plan{
		Env:      "production",
		Services: []string{"api", "web"},
		Releases: map[string]versioning.Release{
			"api": {Version: "v1"},
			"web": {Version: "v2"},
		},
	}
}

// --- newVerifyCommand: required-flag validation ---------------------------

func TestNewVerifyCommand_MissingEnv_FailsBeforeAnyIO(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newVerifyCommand(rt)
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

func TestNewVerifyCommand_ServiceFlagDefault(t *testing.T) {
	rt, _, _ := newTestRuntime()
	cmd := newVerifyCommand(rt)
	f := cmd.Flags().Lookup("service")
	require.NotNil(t, f)
	assert.Equal(t, "", f.DefValue)

	require.NoError(t, cmd.ParseFlags([]string{"--service", "api"}))
	assert.Equal(t, "api", cmd.Flags().Lookup("service").Value.String())
}

// --- emitVerifyText --------------------------------------------------------

func TestEmitVerifyText_AllPass(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	report := &release.VerifyReport{
		Plan: samplePlan(),
		Outcomes: []release.GateOutcome{
			{Service: "api", Pacticipant: "api", Release: versioning.Release{Version: "v1"}, Deployable: true},
			{Service: "web", Pacticipant: "web", Release: versioning.Release{Version: "v2"}, Deployable: true},
		},
	}

	emitVerifyText(rt, report)

	out := stdout.String()
	assert.Contains(t, out, "Resolving versions for production")
	assert.Contains(t, out, "api@v1")
	assert.Contains(t, out, "web@v2")
	assert.Contains(t, out, "can-i-deploy: api@v1 → production")
	assert.Contains(t, out, "safe")
	assert.Contains(t, out, "Summary: 2/2 passed")
}

func TestEmitVerifyText_GateRejection_IncludesReasonAndVerifyURL(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	report := &release.VerifyReport{
		Plan: samplePlan(),
		Outcomes: []release.GateOutcome{
			{
				Service: "api", Pacticipant: "api",
				Release:    versioning.Release{Version: "v1"},
				Deployable: false,
				Reason:     "contracts not compatible",
				VerifyURL:  "http://broker/verify/1",
			},
		},
	}

	emitVerifyText(rt, report)

	out := stdout.String()
	assert.Contains(t, out, "contracts not compatible")
	assert.Contains(t, out, "http://broker/verify/1")
	assert.Contains(t, out, "Summary: 0 passed, 1 failed")
}

func TestEmitVerifyText_OutcomeError_PrintsErrText(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	report := &release.VerifyReport{
		Plan: samplePlan(),
		Outcomes: []release.GateOutcome{
			{Service: "api", Pacticipant: "api", Release: versioning.Release{Version: "v1"}, Err: errors.New("broker unreachable")},
		},
	}

	emitVerifyText(rt, report)

	out := stdout.String()
	assert.Contains(t, out, "broker unreachable")
	assert.Contains(t, out, "Summary: 0 passed, 1 failed")
}

// TestEmitVerifyText_GateRejection_NoVerifyURL proves the "(see URL)" suffix
// is only appended when VerifyURL is actually populated.
func TestEmitVerifyText_GateRejection_NoVerifyURL(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	report := &release.VerifyReport{
		Plan: samplePlan(),
		Outcomes: []release.GateOutcome{
			{Service: "api", Pacticipant: "api", Release: versioning.Release{Version: "v1"}, Deployable: false, Reason: "no pact found"},
		},
	}

	emitVerifyText(rt, report)

	out := stdout.String()
	assert.Contains(t, out, "no pact found")
	assert.NotContains(t, out, "(see ")
}

// --- emitVerifyJSON --------------------------------------------------------

func TestEmitVerifyJSON_Shape(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	report := &release.VerifyReport{
		Plan: samplePlan(),
		Outcomes: []release.GateOutcome{
			{Service: "api", Pacticipant: "api", Release: versioning.Release{Version: "v1", ImageRef: "registry/api:v1"}, Deployable: true, Reason: "ok"},
			{Service: "web", Pacticipant: "web", Release: versioning.Release{Version: "v2"}, Deployable: false, Reason: "not compatible", VerifyURL: "http://broker/2"},
		},
	}

	require.NoError(t, emitVerifyJSON(rt, report))

	var got output.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))

	assert.Equal(t, "production", got.Env)
	assert.Equal(t, "verify", got.Command)
	require.Len(t, got.Results, 2)

	assert.Equal(t, "api", got.Results[0].Service)
	assert.Equal(t, "pass", got.Results[0].Verdict)
	assert.Equal(t, "registry/api:v1", got.Results[0].ImageRef)

	assert.Equal(t, "web", got.Results[1].Service)
	assert.Equal(t, "fail", got.Results[1].Verdict)
	assert.Equal(t, "not compatible", got.Results[1].Reason)
	assert.Equal(t, "http://broker/2", got.Results[1].BrokerURL)

	assert.Equal(t, 1, got.Summary.Pass)
	assert.Equal(t, 1, got.Summary.Fail)
}

// TestEmitVerifyJSON_OutcomeError_VerdictIsError proves an outcome-level
// error (broker unreachable, etc.) is distinguished from a gate rejection:
// verdict "error" with reason from Err, not Reason.
func TestEmitVerifyJSON_OutcomeError_VerdictIsError(t *testing.T) {
	rt, stdout, _ := newTestRuntime()
	report := &release.VerifyReport{
		Plan: samplePlan(),
		Outcomes: []release.GateOutcome{
			{Service: "api", Pacticipant: "api", Release: versioning.Release{Version: "v1"}, Err: errors.New("timeout")},
		},
	}

	require.NoError(t, emitVerifyJSON(rt, report))

	var got output.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	require.Len(t, got.Results, 1)
	assert.Equal(t, "error", got.Results[0].Verdict)
	assert.Equal(t, "timeout", got.Results[0].Reason)
	assert.Equal(t, 0, got.Summary.Pass)
	assert.Equal(t, 1, got.Summary.Fail)
}

// --- joinServices -----------------------------------------------------------

func TestJoinServices_MultipleServices(t *testing.T) {
	got := joinServices(samplePlan())
	assert.Equal(t, "api@v1, web@v2", got)
}

func TestJoinServices_NoServices(t *testing.T) {
	got := joinServices(&release.Plan{Env: "production", Releases: map[string]versioning.Release{}})
	assert.Equal(t, "", got)
}
