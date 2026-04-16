package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const (
	StrategyManifestFile    = "manifest_file"
	StrategyResolvedDigest  = "resolved_image_digest"
	StrategyGitSHA          = "git_sha"
)

var validStrategies = map[string]struct{}{
	StrategyManifestFile:   {},
	StrategyResolvedDigest: {},
	StrategyGitSHA:         {},
}

// rawProbe is used to detect fields that must not appear in YAML (credentials).
type rawProbe struct {
	Broker struct {
		Username *string `yaml:"username"`
		Password *string `yaml:"password"`
		Token    *string `yaml:"token"`
		Auth     any     `yaml:"auth"`
	} `yaml:"broker"`
	PactBroker *any `yaml:"pact_broker"`
}

func (p rawProbe) rejectCredentials() error {
	var bad []string
	if p.Broker.Username != nil {
		bad = append(bad, "broker.username")
	}
	if p.Broker.Password != nil {
		bad = append(bad, "broker.password")
	}
	if p.Broker.Token != nil {
		bad = append(bad, "broker.token")
	}
	if p.Broker.Auth != nil {
		bad = append(bad, "broker.auth")
	}
	if len(bad) > 0 {
		return fmt.Errorf("credentials must not appear in c2quay.yml: %s (use PACT_BROKER_USERNAME/PASSWORD/TOKEN env vars)", strings.Join(bad, ", "))
	}
	if p.PactBroker != nil {
		return errors.New("`pact_broker` is not recognized; use `broker` instead")
	}
	return nil
}

func (c *Config) Validate() error {
	if len(c.Compose.Files) == 0 {
		return errors.New("compose.files: at least one compose file is required")
	}
	if strings.TrimSpace(c.Compose.ProjectName) == "" {
		return errors.New("compose.project_name: required")
	}

	if strings.TrimSpace(c.Broker.BaseURL) == "" {
		return errors.New("broker.base_url: required (or set PACT_BROKER_BASE_URL)")
	}
	if _, err := url.ParseRequestURI(c.Broker.BaseURL); err != nil {
		return fmt.Errorf("broker.base_url: invalid URL: %w", err)
	}

	if c.Versioning.Strategy == "" {
		return errors.New("versioning.strategy: required")
	}
	if _, ok := validStrategies[c.Versioning.Strategy]; !ok {
		return fmt.Errorf("versioning.strategy: %q is not supported (use: manifest_file, resolved_image_digest, or git_sha)", c.Versioning.Strategy)
	}

	if len(c.Environments) == 0 {
		return errors.New("environments: at least one environment is required")
	}
	for name, env := range c.Environments {
		if len(env.Services) == 0 {
			return fmt.Errorf("environments.%s.services: at least one service mapping is required", name)
		}
		for svc, mapping := range env.Services {
			if strings.TrimSpace(mapping.Pacticipant) == "" {
				return fmt.Errorf("environments.%s.services.%s.pacticipant: required", name, svc)
			}
		}
	}

	if c.Deploy.Smoke.Command != "" && c.Deploy.Smoke.Timeout <= 0 {
		return errors.New("deploy.smoke.timeout: must be positive when smoke.command is set")
	}

	return nil
}

// LookupEnvironment returns the environment config and true if present.
func (c *Config) LookupEnvironment(name string) (Environment, bool) {
	env, ok := c.Environments[name]
	return env, ok
}

// newLimitedReader returns a reader over b without an allocation penalty.
func newLimitedReader(b []byte) io.Reader { return bytes.NewReader(b) }
