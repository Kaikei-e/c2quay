package versioning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ManifestFile struct {
	path string
}

func NewManifestFile(path string) *ManifestFile { return &ManifestFile{path: path} }

func (*ManifestFile) Name() string { return "manifest_file" }

type manifestEntry struct {
	Version string `json:"version"`
	Image   string `json:"image"`
}

type manifestDoc struct {
	Services map[string]manifestEntry `json:"services"`
}

func (m *ManifestFile) Resolve(_ context.Context, services []string) (map[string]Release, error) {
	abs, err := filepath.Abs(m.path)
	if err != nil {
		return nil, fmt.Errorf("resolve manifest path: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read manifest %q: %w", abs, err)
	}
	var doc manifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode manifest %q: %w", abs, err)
	}
	out := make(map[string]Release, len(services))
	for _, s := range services {
		entry, ok := doc.Services[s]
		if !ok {
			return nil, fmt.Errorf("manifest %q has no entry for service %q", abs, s)
		}
		if entry.Version == "" {
			return nil, fmt.Errorf("manifest %q: service %q has no version", abs, s)
		}
		out[s] = Release{Version: entry.Version, ImageRef: entry.Image}
	}
	return out, nil
}
