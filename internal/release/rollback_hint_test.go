package release_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Kaikei-e/c2quay/internal/release"
)

// TestRollbackHint_Write_NonRecordFailure_KeepsLegacyNote proves the
// existing behaviour is unchanged when the failure happened before
// record-deployment ever ran (e.g. compose-up): the blanket "was NOT
// called" note is accurate in that case and must still be printed.
func TestRollbackHint_Write_NonRecordFailure_KeepsLegacyNote(t *testing.T) {
	var buf bytes.Buffer
	release.RollbackHint{
		Env:      "production",
		FailedAt: release.StepComposeUp,
		Cause:    errors.New("compose boom"),
	}.Write(&buf)

	out := buf.String()
	assert.Contains(t, out, "record-deployment was NOT called for the failed attempt")
	assert.Contains(t, out, "The broker still records the previous version")
}

// TestRollbackHint_Write_RecordFailure_ItemizesRecordedAndUnrecorded proves
// the fix: when the failure IS at the record step, the blanket note (which
// would be factually wrong for the services that succeeded) is replaced by
// an explicit recorded/unrecorded breakdown.
func TestRollbackHint_Write_RecordFailure_ItemizesRecordedAndUnrecorded(t *testing.T) {
	var buf bytes.Buffer
	results := []release.RecordResult{
		{Service: "api", Pacticipant: "api", Version: "v1", Recorded: true},
		{Service: "web", Pacticipant: "web", Version: "v2", Recorded: false, Err: errors.New("broker 503")},
	}
	release.RollbackHint{
		Env:           "production",
		FailedAt:      release.StepRecord,
		Cause:         &release.RecordDeploymentError{Results: results},
		RecordResults: results,
	}.Write(&buf)

	out := buf.String()
	assert.NotContains(t, out, "record-deployment was NOT called for the failed attempt",
		"the blanket note is factually wrong once some services succeeded and must not appear")
	assert.Contains(t, out, "Recorded")
	assert.Contains(t, out, "api")
	assert.Contains(t, out, "api@v1")
	assert.Contains(t, out, "NOT recorded")
	assert.Contains(t, out, "web@v2")
	assert.Contains(t, out, "broker 503")
}

// TestRollbackHint_Write_RecordFailure_NoResults_FallsBackToLegacyNote
// guards the defensive path: if a caller somehow reaches StepRecord without
// populating RecordResults, we still print something accurate rather than
// an empty/broken section.
func TestRollbackHint_Write_RecordFailure_NoResults_FallsBackToLegacyNote(t *testing.T) {
	var buf bytes.Buffer
	release.RollbackHint{
		Env:      "production",
		FailedAt: release.StepRecord,
		Cause:    errors.New("some record error"),
	}.Write(&buf)

	out := buf.String()
	assert.Contains(t, out, "record-deployment was NOT called for the failed attempt")
}

// TestRollbackHint_Write_RollbackSkippedForImageCaptureFailure proves the
// hint surfaces the specific "auto-rollback was impossible, not just
// unnecessary" case, so an operator reading the hint doesn't mistake it for
// a routine no-op skip.
func TestRollbackHint_Write_RollbackSkippedForImageCaptureFailure(t *testing.T) {
	var buf bytes.Buffer
	release.RollbackHint{
		Env:      "production",
		FailedAt: release.StepComposeUp,
		Cause:    errors.New("compose boom"),
		Rollback: &release.RollbackReport{
			Mode:                   release.RollbackOn,
			Skipped:                true,
			SkipReason:             "pre-deploy snapshot has no recorded images (fresh deploy or render failed)",
			ImageCaptureFailed:     true,
			ImageCaptureFailReason: "docker compose config --format json failed: daemon unreachable",
		},
	}.Write(&buf)

	out := buf.String()
	assert.Contains(t, out, "skipped")
	assert.Contains(t, out, "daemon unreachable")
	assert.Contains(t, out, "not possible")
}
