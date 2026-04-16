# ADR 0005 — Go 1.25 as the support floor

Date: 2026-04-17
Status: Accepted

## Context

Go's official support policy keeps the two most recent releases. As of
April 2026 that is Go 1.26 (released 2026-02) and Go 1.25. Pact-Go v2.4.1
requires Go 1.23 minimum. Most CI pipelines are still pinned to 1.25.

## Decision

- Development baseline: Go 1.26.2.
- Support floor: Go 1.25.0.
- `go.mod` declares `go 1.25.0`, which Go 1.26's new `go mod init`
  convention produces automatically.
- CI matrix: `1.25.x` and `1.26.x`.

## Consequences

- c2quay runs on every Go version currently supported by the Go team.
- `testing/synctest`, `log/slog` `GroupAttrs`, and similar 1.25-stabilized
  features are available in both baseline and floor.
- Features that only landed in 1.26 (e.g., `new(expr)`) are not used in
  shipped code; internal tooling may use them.
- When Go 1.27 ships, the floor will move to 1.26 following the same
  policy.
