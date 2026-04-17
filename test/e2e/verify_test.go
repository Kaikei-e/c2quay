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
// given can-i-deploy verdict for every service. When the matrix endpoint
// receives a bracket-array query (aggregate mode used by all_or_nothing),
// it emits one consumer/provider row per selector pair so per-pacticipant
// verdicts have rows to attribute to.
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
		q := r.URL.Query()
		payload := map[string]any{
			"summary": map[string]any{"deployable": &d, "reason": reason},
		}
		if pacts, versions := q["q[][pacticipant]"], q["q[][version]"]; len(pacts) > 0 && len(pacts) == len(versions) {
			rows := make([]map[string]any, 0, len(pacts))
			for i, p := range pacts {
				verified := d
				rows = append(rows, map[string]any{
					"consumer": map[string]any{"name": p, "version": map[string]any{"number": versions[i]}},
					"provider": map[string]any{"name": p + "-partner", "version": map[string]any{"number": versions[i]}},
					"verificationResult": map[string]any{
						"success": verified,
						"_links":  map[string]any{"self": map[string]any{"href": srv.URL + "/v/" + p}},
					},
				})
			}
			payload["matrix"] = rows
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// brokerWithoutMatrix is the same as fakeBroker but omits the
// pb:can-i-deploy relation. Used to assert that all_or_nothing refuses to
// run when the broker cannot serve aggregate queries.
func brokerWithoutMatrix(t *testing.T) *httptest.Server {
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
				"pb:can-i-deploy-pacticipant-version-to-environment": map[string]any{
					"href":      srv.URL + "/cid/provider/{pacticipant}/version/{version}/to-environment/{environment}",
					"templated": true,
				},
			},
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

// all_or_nothing is set in the fixture, so verify must batch into one
// matrix request. If the fix regresses, the broker would see two separate
// /matrix calls.
func TestE2E_Verify_AllOrNothing_SingleMatrixCall(t *testing.T) {
	bin := buildBinary(t)
	var calls int
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
				"pb:can-i-deploy": map[string]any{"href": srv.URL + "/matrix", "templated": false},
			},
		})
	})
	mux.HandleFunc("/matrix", func(w http.ResponseWriter, r *http.Request) {
		calls++
		pacts := r.URL.Query()["q[][pacticipant]"]
		versions := r.URL.Query()["q[][version]"]
		assert.Len(t, pacts, 2)
		assert.Len(t, versions, 2)
		assert.Equal(t, "production", r.URL.Query().Get("environment"))
		tr := true
		rows := []map[string]any{}
		for i, p := range pacts {
			rows = append(rows, map[string]any{
				"consumer":           map[string]any{"name": p, "version": map[string]any{"number": versions[i]}},
				"provider":           map[string]any{"name": p + "-partner", "version": map[string]any{"number": versions[i]}},
				"verificationResult": map[string]any{"success": true, "_links": map[string]any{"self": map[string]any{"href": "http://v/" + p}}},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"summary": map[string]any{"deployable": &tr, "reason": "ok"},
			"matrix":  rows,
		})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	code, out, errOut := runC2Q(t, bin, fixtureDir(t), srv.URL, "verify", "--env", "production")
	assert.Equal(t, 0, code, "stderr=%s stdout=%s", errOut, out)
	assert.Equal(t, 1, calls, "all_or_nothing must issue exactly one matrix request")
}

// When the broker only exposes the scoped (single-pacticipant) relation,
// all_or_nothing should refuse to run rather than silently fall back to
// per-service queries, which is what reintroduced the production incident.
func TestE2E_Verify_AllOrNothing_RelationMissing(t *testing.T) {
	bin := buildBinary(t)
	srv := brokerWithoutMatrix(t)
	dir := fixtureDir(t)

	code, out, errOut := runC2Q(t, bin, dir, srv.URL, "verify", "--env", "production")
	assert.NotEqual(t, 0, code)
	combined := out + errOut
	assert.Contains(t, combined, "all_or_nothing")
	assert.Contains(t, combined, "pb:can-i-deploy")
}
