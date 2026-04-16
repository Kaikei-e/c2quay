package logging_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/logging"
)

func TestMultiHandler_WritesToBothStderrAndAudit(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")

	var stderrBuf bytes.Buffer
	l, err := logging.New(logging.Options{
		Level:    "info",
		AuditLog: auditPath,
		Stderr:   &stderrBuf,
	})
	require.NoError(t, err)

	l.Info("gate check completed", "step", "gate")

	stderrOut := stderrBuf.String()
	assert.Contains(t, stderrOut, "gate check completed", "stderr should contain the message")

	raw, err := os.ReadFile(auditPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"msg":"gate check completed"`, "audit log should contain JSON record; got=%q", string(raw))
	assert.Contains(t, string(raw), `"step":"gate"`)
}
