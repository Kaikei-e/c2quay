# ADR 0003 — Compose CLI as source of truth

Date: 2026-04-17
Status: Accepted

## Context

c2quay needs to know which services exist in a Compose project and which
image each one resolves to. Two options are available:

1. Parse `compose.yaml` ourselves (via `compose-go` or bespoke YAML
   handling).
2. Shell out to `docker compose config --format json` and trust the user's
   installed Compose to resolve everything.

Option 1 lets us move fast without subprocess overhead. But the Compose
Specification keeps extending — `include` with OCI references, the `models`
top-level, new merge semantics, environment variable rules. Tracking all of
that forever is outside c2quay's mission and creates a source of
interpretation drift between c2quay and the Compose the user actually runs.

## Decision

c2quay always shells out to `docker compose` for config resolution, service
enumeration, image extraction, and container state. The `ShellAdapter`
captures this behind an `Adapter` interface so a future SDK-backed
implementation can be substituted without touching callers.

## Consequences

- Whatever `docker compose config --format json` outputs on the user's
  machine is exactly what c2quay sees — no interpretation drift.
- c2quay requires `docker compose >= v2.40.2` (CVE-2025-62725 fix).
- Tests shell out to real Compose for integration coverage; unit tests mock
  the `Exec` interface.
- Compose v1 (`docker-compose` with a hyphen) is explicitly rejected; it
  was removed upstream in 2025-04.
