package versioning

import (
	"context"
	"fmt"
	"strings"

	"github.com/Kaikei-e/c2quay/internal/composeadapter"
)

// configRenderer is the minimal Compose surface the resolved_image_digest
// strategy depends on. Defined here (consumer-side) so we are not coupled
// to the whole composeadapter.Adapter interface.
type configRenderer interface {
	RenderConfigJSON(ctx context.Context) (*composeadapter.RenderedConfig, error)
}

// ResolvedDigest resolves each service's release identifier as the sha256
// digest embedded in its Compose image reference.
type ResolvedDigest struct {
	adapter configRenderer
}

// NewResolvedDigest builds a ResolvedDigest strategy backed by a Compose
// adapter (or anything that renders a compose config JSON).
func NewResolvedDigest(a configRenderer) *ResolvedDigest {
	return &ResolvedDigest{adapter: a}
}

func (*ResolvedDigest) Name() string { return "resolved_image_digest" }

func (r *ResolvedDigest) Resolve(ctx context.Context, services []string) (map[string]Release, error) {
	rc, err := r.adapter.RenderConfigJSON(ctx)
	if err != nil {
		return nil, err
	}
	images := rc.ImagesByService()

	out := make(map[string]Release, len(services))
	for _, s := range services {
		img, ok := images[s]
		if !ok || img == "" {
			return nil, fmt.Errorf("resolved_image_digest: service %q has no image", s)
		}
		digest, err := extractDigest(img)
		if err != nil {
			return nil, fmt.Errorf("resolved_image_digest: service %q: %w", s, err)
		}
		out[s] = Release{Version: digest, ImageRef: img}
	}
	return out, nil
}

// extractDigest returns the "sha256:..." portion from an image reference.
// Mutable tag-only references are rejected.
func extractDigest(ref string) (string, error) {
	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return "", fmt.Errorf("image %q is tag-only; c2quay requires digest-pinned references (image@sha256:...)", ref)
	}
	digest := ref[at+1:]
	if !strings.HasPrefix(digest, "sha256:") || len(digest) < len("sha256:")+8 {
		return "", fmt.Errorf("image %q: unrecognized digest %q", ref, digest)
	}
	return digest, nil
}
