package composeadapter

import (
	"encoding/json"
	"fmt"
)

// ParseRenderedConfig decodes the output of `docker compose config --format json`.
// Only the fields c2quay needs are extracted.
func ParseRenderedConfig(raw []byte) (*RenderedConfig, error) {
	var rc RenderedConfig
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, fmt.Errorf("decode compose config json: %w", err)
	}
	if rc.Services == nil {
		rc.Services = map[string]RenderedService{}
	}
	return &rc, nil
}

// ImagesByService returns a service-name to image-ref mapping for all
// services that have an image field. Services without image are skipped.
func (rc *RenderedConfig) ImagesByService() map[string]string {
	out := make(map[string]string, len(rc.Services))
	for name, svc := range rc.Services {
		if svc.Image != "" {
			out[name] = svc.Image
		}
	}
	return out
}
