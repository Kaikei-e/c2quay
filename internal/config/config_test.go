package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/config"
)

func TestLoad_Valid(t *testing.T) {
	t.Setenv("PACT_BROKER_BASE_URL", "")
	t.Setenv("PACT_BROKER_USERNAME", "u")
	t.Setenv("PACT_BROKER_PASSWORD", "p")

	cfg, err := config.Load("testdata/valid.yml")
	require.NoError(t, err)

	assert.Equal(t, "myapp", cfg.Compose.ProjectName)
	assert.Equal(t, []string{"compose.yaml"}, cfg.Compose.Files)
	assert.Equal(t, "https://pact-broker.example.com", cfg.Broker.BaseURL)
	assert.Equal(t, "u", cfg.Broker.Username())
	assert.Equal(t, "p", cfg.Broker.Password())
	assert.Equal(t, config.StrategyManifestFile, cfg.Versioning.Strategy)
	assert.Equal(t, 180*time.Second, cfg.Deploy.WaitTimeout)
	assert.Equal(t, 30*time.Second, cfg.Deploy.Smoke.Timeout)

	env, ok := cfg.LookupEnvironment("production")
	require.True(t, ok)
	assert.True(t, env.AllOrNothing)
	assert.Equal(t, "api", env.Services["api"].Pacticipant)
}

func TestLoad_RejectsCredentialsInYAML(t *testing.T) {
	_, err := config.Load("testdata/credentials.yml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credentials must not appear")
}

func TestLoad_RejectsLegacyPactBrokerKey(t *testing.T) {
	_, err := config.Load("testdata/pact_broker_legacy.yml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pact_broker")
}

func TestLoad_RejectsUnsupportedStrategy(t *testing.T) {
	_, err := config.Load("testdata/bad_strategy.yml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image_tag")
}

func TestLoad_EnvBaseURLOverrides(t *testing.T) {
	t.Setenv("PACT_BROKER_BASE_URL", "https://env.example.com")
	cfg, err := config.Load("testdata/valid.yml")
	require.NoError(t, err)
	assert.Equal(t, "https://env.example.com", cfg.Broker.BaseURL)
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load("testdata/does-not-exist.yml")
	require.Error(t, err)
}

func TestValidate_RequiresComposeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yml")
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(readFile(t, "testdata/valid.yml"), "- compose.yaml", "")), 0o600))
	_, err := config.Load(path)
	require.Error(t, err)
}

// TestLoad_PullPolicyDefaultsToNever proves ADR 0010: unset `deploy.pull`
// must default to "never" so existing configs keep their prior behaviour.
func TestLoad_PullPolicyDefaultsToNever(t *testing.T) {
	cfg, err := config.Load("testdata/valid.yml")
	require.NoError(t, err)
	assert.Equal(t, config.PullNever, cfg.Deploy.Pull)
}

func TestLoad_PullPolicyAcceptsAllowedValues(t *testing.T) {
	for _, v := range []string{config.PullAlways, config.PullMissing, config.PullNever} {
		dir := t.TempDir()
		path := filepath.Join(dir, "c.yml")
		body := strings.Replace(readFile(t, "testdata/valid.yml"), "  wait: true", "  wait: true\n  pull: "+v, 1)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		cfg, err := config.Load(path)
		require.NoError(t, err, "value=%s", v)
		assert.Equal(t, v, cfg.Deploy.Pull, "value=%s", v)
	}
}

func TestLoad_PullPolicyRejectsUnknownValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yml")
	body := strings.Replace(readFile(t, "testdata/valid.yml"), "  wait: true", "  wait: true\n  pull: sometimes", 1)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploy.pull")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}
