package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level shape of c2quay.yml.
type Config struct {
	Compose      ComposeConfig          `yaml:"compose"`
	Broker       BrokerConfig           `yaml:"broker"`
	Versioning   VersioningConfig       `yaml:"versioning"`
	Deploy       DeployConfig           `yaml:"deploy"`
	Environments map[string]Environment `yaml:"environments"`

	// Path is the absolute path of the file this config was loaded from.
	Path string `yaml:"-"`
}

// ComposeConfig selects which Compose files and project name c2quay drives.
type ComposeConfig struct {
	Files       []string `yaml:"files"`
	ProjectName string   `yaml:"project_name"`
}

// BrokerConfig holds the broker endpoint. Credentials live in env vars only.
type BrokerConfig struct {
	BaseURL string `yaml:"base_url"`

	// Auth fields are populated from environment variables at Load time,
	// never from the YAML document. Keeping them unexported prevents
	// accidental marshalling.
	username string `yaml:"-"`
	password string `yaml:"-"`
	token    string `yaml:"-"`
}

func (b BrokerConfig) Username() string { return b.username }
func (b BrokerConfig) Password() string { return b.password }
func (b BrokerConfig) Token() string    { return b.token }

// VersioningConfig picks one of the supported release-identity strategies.
type VersioningConfig struct {
	Strategy string            `yaml:"strategy"`
	Options  map[string]string `yaml:"options"`
}

// DeployConfig tunes the deploy pipeline.
type DeployConfig struct {
	Wait        bool          `yaml:"wait"`
	WaitTimeout time.Duration `yaml:"wait_timeout"`
	// Pull controls whether c2quay runs `docker compose pull` between the
	// gate and `compose up`. Values: "never" (default), "always", "missing".
	// See ADR 0010.
	Pull  string      `yaml:"pull"`
	Smoke SmokeConfig `yaml:"smoke"`
}

// SmokeConfig describes the optional post-deploy smoke script.
type SmokeConfig struct {
	Command string            `yaml:"command"`
	Timeout time.Duration     `yaml:"timeout"`
	Env     map[string]string `yaml:"env"`
}

// Environment maps logical services to pacticipants in the broker.
type Environment struct {
	AllOrNothing bool                      `yaml:"all_or_nothing"`
	Services     map[string]ServiceMapping `yaml:"services"`
}

// ServiceMapping is a Compose-service → Pact-pacticipant link.
type ServiceMapping struct {
	Pacticipant string `yaml:"pacticipant"`
}

// Load reads and validates a c2quay config file.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}

	var probe rawProbe
	if err := yaml.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := probe.rejectCredentials(); err != nil {
		return nil, err
	}

	var cfg Config
	dec := yaml.NewDecoder(newLimitedReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	cfg.Path = abs
	cfg.applyDefaults()
	cfg.mergeEnv()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Deploy.WaitTimeout == 0 {
		c.Deploy.WaitTimeout = 180 * time.Second
	}
	if c.Deploy.Pull == "" {
		c.Deploy.Pull = PullNever
	}
	if c.Deploy.Smoke.Timeout == 0 && c.Deploy.Smoke.Command != "" {
		c.Deploy.Smoke.Timeout = 30 * time.Second
	}
	for name, env := range c.Environments {
		if env.Services == nil {
			env.Services = map[string]ServiceMapping{}
			c.Environments[name] = env
		}
	}
}

func (c *Config) mergeEnv() {
	if v := os.Getenv("PACT_BROKER_BASE_URL"); v != "" {
		c.Broker.BaseURL = v
	}
	c.Broker.username = os.Getenv("PACT_BROKER_USERNAME")
	c.Broker.password = os.Getenv("PACT_BROKER_PASSWORD")
	c.Broker.token = os.Getenv("PACT_BROKER_TOKEN")
}
