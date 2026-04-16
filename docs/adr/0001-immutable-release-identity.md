# ADR 0001 — Immutable release identity only

Date: 2026-04-17
Status: Accepted

## Context

Pact Broker's `can-i-deploy` is built on the assumption that a pacticipant
version is immutable: the same version number always refers to the same
contract content. If c2quay resolves a release as a mutable reference (a
floating tag such as `latest` or an image tag like `api:main`), then two
different `can-i-deploy` calls with the same input can legitimately refer to
two different contracts, and the gate becomes meaningless.

## Decision

c2quay v0 supports exactly three versioning strategies:

- `manifest_file` — a CI-produced JSON mapping each service to an immutable
  `{version, image}`. **Recommended default.**
- `resolved_image_digest` — extracted from `docker compose config --format
  json`; only `image@sha256:...` references are accepted. Tag-only references
  are a hard error.
- `git_sha` — `git rev-parse HEAD`; useful for monorepos where every service
  is cut from the same commit.

`image_tag` as a strategy is explicitly rejected. The safety cost outweighs
the convenience.

## Consequences

- Teams using mutable tags must migrate to digest-pinned images or adopt
  `manifest_file`. This is a one-time migration.
- `record-deployment` reliably points to the version that was actually
  deployed, closing the loop for the next `can-i-deploy`.
- If the need ever arises, `immutable_image_tag` could be added in the future
  as a strict variant of `image_tag` that rejects `latest` and any tag
  matching a deny-list. That is not in scope for v0.
