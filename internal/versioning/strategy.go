// Package versioning resolves the release identifier (immutable version + image
// ref) for each service that c2quay is about to gate.
//
// c2quay intentionally refuses mutable identifiers. Only `manifest_file`,
// `resolved_image_digest`, and `git_sha` are supported. `image_tag` was
// considered and rejected — see docs/adr/0001-immutable-release-identity.md.
package versioning

import (
	"context"
	"errors"
	"fmt"

	"github.com/Kaikei-e/c2quay/internal/config"
)

// Release is the resolved identity of a service for a given deploy.
type Release struct {
	Version  string // immutable identifier (SHA, digest, or manifest-provided version)
	ImageRef string // optional — the resolved image reference if known
}

// Strategy resolves release identifiers for a set of services.
type Strategy interface {
	Name() string
	Resolve(ctx context.Context, services []string) (map[string]Release, error)
}

// Factory builds the Strategy for a given config. The adapter is only used
// by the resolved_image_digest strategy; other strategies ignore it.
func Factory(cfg *config.Config, adapter configRenderer) (Strategy, error) {
	switch cfg.Versioning.Strategy {
	case config.StrategyManifestFile:
		path := cfg.Versioning.Options["path"]
		if path == "" {
			return nil, errors.New("versioning.options.path: required for manifest_file strategy")
		}
		return NewManifestFile(path), nil
	case config.StrategyResolvedDigest:
		return NewResolvedDigest(adapter), nil
	case config.StrategyGitSHA:
		return NewGitSHA(), nil
	default:
		return nil, fmt.Errorf("versioning.strategy: %q is not supported", cfg.Versioning.Strategy)
	}
}
