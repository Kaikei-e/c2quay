package output_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Kaikei-e/c2quay/internal/output"
)

// TestNewText_ImplementsWriter is a compile-time-ish smoke test that
// NewText's concrete type satisfies the Writer interface other packages
// depend on (release.UI, doctor.Render, etc.).
func TestNewText_ImplementsWriter(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	assert.Implements(t, (*output.Writer)(nil), w)
}

func TestTextWriter_Step_WithDetail(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Step("plan", "2 service(s)")
	assert.Equal(t, "→ plan: 2 service(s)\n", buf.String())
}

func TestTextWriter_Ok_WithDetail(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Ok("can-i-deploy: api", "safe")
	assert.Equal(t, "✓ can-i-deploy: api: safe\n", buf.String())
}

func TestTextWriter_Fail_WithDetail(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Fail("can-i-deploy: api", "contracts not compatible")
	assert.Equal(t, "✗ can-i-deploy: api: contracts not compatible\n", buf.String())
}

func TestTextWriter_Warn_WithDetail(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Warn("rollback", "skipped: nothing to do")
	assert.Equal(t, "! rollback: skipped: nothing to do\n", buf.String())
}

// TestTextWriter_Line_EmptyDetail_OmitsColon proves the "label only" shape
// used by e.g. dry-run notices with no extra detail.
func TestTextWriter_Line_EmptyDetail_OmitsColon(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Warn("dry-run", "")
	assert.Equal(t, "! dry-run\n", buf.String())
}

// TestTextWriter_Line_WhitespaceOnlyDetail_TreatedAsEmpty proves detail is
// trimmed before the empty check, so "   " behaves like "".
func TestTextWriter_Line_WhitespaceOnlyDetail_TreatedAsEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Ok("environment lock", "   ")
	assert.Equal(t, "✓ environment lock\n", buf.String())
}

// TestTextWriter_Line_DetailIsTrimmed proves leading/trailing whitespace in
// detail is stripped even when detail is otherwise non-empty.
func TestTextWriter_Line_DetailIsTrimmed(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Step("plan", "  2 service(s)  ")
	assert.Equal(t, "→ plan: 2 service(s)\n", buf.String())
}

func TestTextWriter_Summary_AllPassed(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Summary(3, 0)
	assert.Equal(t, "\nSummary: 3/3 passed\n", buf.String())
}

func TestTextWriter_Summary_SomeFailed(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Summary(2, 1)
	assert.Equal(t, "\nSummary: 2 passed, 1 failed\n", buf.String())
}

func TestTextWriter_Summary_AllFailed(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Summary(0, 4)
	assert.Equal(t, "\nSummary: 0 passed, 4 failed\n", buf.String())
}

// TestTextWriter_MultipleLines_Accumulate proves successive calls append
// rather than overwrite — the shape a real command's output relies on.
func TestTextWriter_MultipleLines_Accumulate(t *testing.T) {
	var buf bytes.Buffer
	w := output.NewText(&buf)
	w.Step("plan", "1 service(s)")
	w.Ok("can-i-deploy: api", "v1")
	w.Summary(1, 0)

	want := "→ plan: 1 service(s)\n" +
		"✓ can-i-deploy: api: v1\n" +
		"\nSummary: 1/1 passed\n"
	assert.Equal(t, want, buf.String())
}
