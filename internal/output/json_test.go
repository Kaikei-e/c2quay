package output_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/output"
)

// TestWriteJSON_RoundTrips proves a fully-populated Report survives an
// encode/decode cycle and lands in the exact JSON shape the e2e tests (and
// any downstream consumer of `--output json`) rely on.
func TestWriteJSON_RoundTrips(t *testing.T) {
	report := output.Report{
		Env:     "production",
		Command: "verify",
		Results: []output.ServiceResult{
			{
				Service:     "api",
				Pacticipant: "api",
				Version:     "abc123",
				ImageRef:    "registry/api:abc123",
				Verdict:     "pass",
				Reason:      "",
				BrokerURL:   "http://broker/verify/1",
			},
			{
				Service:     "web",
				Pacticipant: "web",
				Version:     "def456",
				Verdict:     "fail",
				Reason:      "contracts not compatible",
			},
		},
		Summary:  output.ReportSummary{Pass: 1, Fail: 1},
		AuditURL: "http://broker/audit/1",
	}

	var buf bytes.Buffer
	require.NoError(t, output.WriteJSON(&buf, report))

	var got output.Report
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, report, got)
}

// TestWriteJSON_FieldNames locks the wire schema — external tooling (or the
// e2e tests) parse this by field name, so a rename here is a breaking change
// that must fail a test, not just a code review.
func TestWriteJSON_FieldNames(t *testing.T) {
	report := output.Report{
		Env:     "staging",
		Command: "deploy",
		Results: []output.ServiceResult{
			{Service: "api", Pacticipant: "api", Version: "v1", Verdict: "pass"},
		},
		Summary: output.ReportSummary{Pass: 1, Fail: 0},
	}

	var buf bytes.Buffer
	require.NoError(t, output.WriteJSON(&buf, report))

	var raw map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &raw))

	assert.Equal(t, "staging", raw["env"])
	assert.Equal(t, "deploy", raw["command"])
	assert.Contains(t, raw, "results")
	assert.Contains(t, raw, "summary")
	assert.NotContains(t, raw, "audit_url", "omitempty must drop an unset AuditURL")

	summary, ok := raw["summary"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), summary["pass"])
	assert.Equal(t, float64(0), summary["fail"])

	results, ok := raw["results"].([]any)
	require.True(t, ok)
	require.Len(t, results, 1)
	row, ok := results[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "api", row["service"])
	assert.Equal(t, "api", row["pacticipant"])
	assert.Equal(t, "v1", row["version"])
	assert.Equal(t, "pass", row["verdict"])
	assert.NotContains(t, row, "reason", "omitempty must drop an unset Reason")
	assert.NotContains(t, row, "image_ref", "omitempty must drop an unset ImageRef")
	assert.NotContains(t, row, "broker_url", "omitempty must drop an unset BrokerURL")
}

// TestWriteJSON_Indented proves the output is pretty-printed (2-space
// indent), matching what emitVerifyJSON has always produced for operators
// reading `c2quay verify --output json` directly.
func TestWriteJSON_Indented(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, output.WriteJSON(&buf, output.Report{Env: "e", Command: "verify"}))
	assert.Contains(t, buf.String(), "\n  \"env\": \"e\"")
}

// TestWriteJSON_EmptyResults proves a report with zero results (e.g. an
// environment with no mapped services) still encodes valid, well-shaped
// JSON rather than omitting the key or erroring.
func TestWriteJSON_EmptyResults(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, output.WriteJSON(&buf, output.Report{Env: "e", Command: "verify"}))

	var got output.Report
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Empty(t, got.Results)
	assert.Equal(t, output.ReportSummary{}, got.Summary)
}
