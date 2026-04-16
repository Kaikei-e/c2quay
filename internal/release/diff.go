package release

import (
	"sort"

	"github.com/Kaikei-e/c2quay/internal/versioning"
)

// ServiceDelta describes how a single service's release changed between two snapshots.
type ServiceDelta struct {
	Service string
	Before  versioning.Release
	After   versioning.Release
	Changed bool
}

// Diff compares two snapshots by release identity.
func Diff(pre, post *Snapshot) []ServiceDelta {
	services := make(map[string]struct{})
	for s := range pre.Releases {
		services[s] = struct{}{}
	}
	for s := range post.Releases {
		services[s] = struct{}{}
	}
	out := make([]ServiceDelta, 0, len(services))
	for s := range services {
		before := pre.Releases[s]
		after := post.Releases[s]
		out = append(out, ServiceDelta{
			Service: s,
			Before:  before,
			After:   after,
			Changed: before.Version != after.Version,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}
