//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "fixtures", "minimal")
}

func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "c2quay")
	// Build from the repo root (one level up from test/e2e).
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/c2quay")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", string(out))
	return bin
}

// fakeBroker returns an httptest.Server that answers the HAL flow with the
// given can-i-deploy verdict for every service.
func fakeBroker(t *testing.T, deployable bool, reason string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/hal+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"_links": map[string]any{
				"pb:can-i-deploy":      map[string]any{"href": srv.URL + "/matrix", "templated": false},
				"pb:record-deployment": map[string]any{"href": srv.URL + "/rec", "templated": false},
				"pb:environments":      map[string]any{"href": srv.URL + "/environments", "templated": false},
			},
		})
	})
	mux.HandleFunc("/matrix", func(w http.ResponseWriter, r *http.Request) {
		d := deployable
		_ = json.NewEncoder(w).Encode(map[string]any{
			"summary": map[string]any{"deployable": &d, "reason": reason},
		})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func runC2Q(t *testing.T, bin, dir, brokerURL string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Env, "PATH=/usr/bin:/bin:/usr/local/bin")
	cmd.Env = append(cmd.Env, "PACT_BROKER_BASE_URL="+brokerURL)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
	return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

func TestE2E_Verify_Pass(t *testing.T) {
	bin := buildBinary(t)
	srv := fakeBroker(t, true, "ok")
	dir := fixtureDir(t)

	code, out, errOut := runC2Q(t, bin, dir, srv.URL, "verify", "--env", "production")
	assert.Equal(t, 0, code, "stderr=%s stdout=%s", errOut, out)
	assert.Contains(t, out, "can-i-deploy")
	assert.Contains(t, out, "Summary: 2/2 passed")
}

func TestE2E_Verify_Fail(t *testing.T) {
	bin := buildBinary(t)
	srv := fakeBroker(t, false, "pact broken")
	dir := fixtureDir(t)

	code, out, errOut := runC2Q(t, bin, dir, srv.URL, "verify", "--env", "production")
	assert.Equal(t, 1, code, "expected gate failure exit code")
	combined := out + errOut
	assert.True(t, strings.Contains(combined, "pact broken") || strings.Contains(combined, "gated"),
		"expected failure message; got: %s", combined)
}

func TestE2E_Verify_JSONOutput(t *testing.T) {
	bin := buildBinary(t)
	srv := fakeBroker(t, true, "ok")
	dir := fixtureDir(t)

	code, out, errOut := runC2Q(t, bin, dir, srv.URL, "verify", "--env", "production", "--output", "json")
	require.Equal(t, 0, code, "stderr=%s", errOut)
	var r map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &r), "stdout=%s", out)
	assert.Equal(t, "production", r["env"])
	assert.Equal(t, "verify", r["command"])
}
