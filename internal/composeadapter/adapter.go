// Package composeadapter hides everything that talks to `docker compose`.
//
// The v0 implementation shells out. A future SDK-backed implementation can
// satisfy the same Adapter interface without touching callers.
package composeadapter

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Kaikei-e/c2quay/internal/doctor"
)

type Adapter interface {
	// Version returns the installed `docker compose` version.
	Version(ctx context.Context) (VersionInfo, error)

	// Validate runs `docker compose config --quiet` to confirm the file is valid.
	Validate(ctx context.Context) error

	// RenderConfigJSON returns the fully-resolved project configuration.
	// This is the source of truth for services and image references.
	RenderConfigJSON(ctx context.Context) (*RenderedConfig, error)

	// ConfigServices returns the service names in the resolved Compose
	// config (`docker compose config --services`). Used by plan-time
	// validation to confirm every non-gate_only mapped service actually
	// exists in Compose before the Pact gate runs. See ADR 0013.
	ConfigServices(ctx context.Context) ([]string, error)

	// PsJSON returns the current container states for the project.
	PsJSON(ctx context.Context) ([]ContainerStatus, error)

	// Pull runs `docker compose pull <services>`, streaming progress to the
	// supplied writer. Empty services means "every service in the project".
	// See ADR 0010.
	Pull(ctx context.Context, services []string, progress io.Writer) error

	// Up runs `docker compose up -d` with the configured options, streaming
	// progress output to progress.
	Up(ctx context.Context, opts UpOptions, progress io.Writer) error
}

// VersionInfo reports the installed Compose version and the capabilities
// c2quay cares about.
type VersionInfo struct {
	Raw             string
	Parsed          doctor.ComposeVersion
	SupportsWait    bool
	SupportsJSONOut bool
	IsHyphenated    bool
}

// RenderedConfig is a trimmed projection of `docker compose config --format json`.
// We only pull the fields c2quay needs.
type RenderedConfig struct {
	Name     string                     `json:"name"`
	Services map[string]RenderedService `json:"services"`
}

// RenderedService is the sliver of a Compose service definition c2quay uses.
type RenderedService struct {
	Image string `json:"image"`
}

// ContainerStatus is a projection of `docker compose ps --format json`.
type ContainerStatus struct {
	Name     string `json:"Name"`
	Service  string `json:"Service"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	Status   string `json:"Status"`
	ExitCode int    `json:"ExitCode"`
}

// UpOptions controls `docker compose up -d`.
type UpOptions struct {
	Services      []string
	RemoveOrphans bool
	Wait          bool
	Timeout       time.Duration
	// ForceRecreate passes `--force-recreate` to compose up. Intended as a
	// per-deploy debug escape hatch for the fresh-build-same-tag case where
	// Compose's digest-diff misfires. See ADR 0011.
	ForceRecreate bool
	// ExtraFiles are appended after the adapter's base compose files for this
	// single call. Used by the rollback flow to inject a pinned-image override
	// without rebuilding the adapter.
	ExtraFiles []string
}

// ErrHyphenatedCompose indicates the caller invoked `docker-compose` (v1),
// which c2quay explicitly does not support.
var ErrHyphenatedCompose = errors.New("docker-compose (hyphen form) is not supported; install Compose v2/v5 and use `docker compose`")
