# ADR 0008 — Pact Broker HAL: scoped relations and two-stage record-deployment

Date: 2026-04-17
Status: Accepted
Supersedes parts of: [ADR 0002](0002-hal-driven-broker-client.md) (implementation detail, not the HAL-driven principle)

## Context

ADR 0002 committed c2quay to HAL navigation from the broker's index
resource rather than hard-coding URLs. v0.3.0 – v0.4.2 named two
specific relations on that index and used them directly:

- `pb:can-i-deploy` — treated as a query-string endpoint (`/matrix?pacticipant=&version=&environment=`).
- `pb:record-deployment` — expected at the root index, templated with `{pacticipant}/{version}/{environment}`.

Testing against a real, current Pact Broker showed both assumptions are
wrong for modern versions:

1. The root index no longer exposes a generic `pb:can-i-deploy`. It
   exposes scope-specific relations instead:
   - `pb:can-i-deploy-pacticipant-version-to-environment` — path-templated,
     takes `{pacticipant}`, `{version}`, `{environment}` in the URL path,
     does not use a query string.
   - `pb:can-i-deploy-pacticipant-version-to-tag` — the tag-scoped sibling.
2. The root index does not expose `pb:record-deployment` either. That
   relation lives under `pb:pacticipant-version` — the operator must first
   follow `pb:pacticipant-version` (templated with `{pacticipant}/{version}`),
   GET that resource, and then POST to the nested `pb:record-deployment`
   link (which itself is templated with `{environment}`).
3. The POST body for `pb:record-deployment` **does not** accept
   `{environment: "..."}`. Environment is a path parameter. Legal body
   fields are `applicationInstance` and `replacedPreviousDeployedVersion`.
4. HAL `_links` values are not uniformly single objects: the spec
   mandates that `curies` is always an array, and brokers are free to
   return arrays for any relation. A `map[string]Link` decode blows up on
   `curies`.

Additionally, the `c2quay verify` pre-flight check performed its own
`HasRelation("pb:can-i-deploy")` probe separate from the broker-client
layer, which meant even after fixing the client, `verify` still rejected
modern brokers.

## Decision

1. **Scoped can-i-deploy first, legacy as fallback.** `Client.CanIDeploy`
   tries `pb:can-i-deploy-pacticipant-version-to-environment` first,
   expanding all three variables into the URL path and sending no query
   parameters. If that relation is absent, it falls back to the legacy
   `pb:can-i-deploy` using the old query-string form. If neither relation
   is exposed, `ErrRelationMissing` names both attempted relations.

2. **Two-stage `record-deployment` navigation.** `Client.RecordDeployment`
   checks for a root-level `pb:record-deployment` first (older forks).
   When absent, it expands `pb:pacticipant-version`, fetches the resource,
   and re-expands the nested `pb:record-deployment` with the remaining
   `{environment}` variable. If neither path is available,
   `ErrRelationMissing` again names both attempted relations.

3. **Correct POST body shape.** `RecordDeploymentInput` drops the
   `{environment}` body field. It gains
   `ReplacedPreviousDeployedVersion *bool`. Only fields the broker
   actually accepts are serialised; nil pointers are omitted so the broker
   applies its own defaults.

4. **Polymorphic `_links` decoding.** `Index.UnmarshalJSON` accepts per-
   relation arrays as well as objects. `curies` is stored on a dedicated
   `Index.Curies` field and excluded from the `Links` map. Any other
   array-valued relation is stored in full in `MultiLinks[rel]` and its
   first element is also placed in `Links[rel]` so existing single-link
   callers keep working. Malformed values (scalars, nulls, etc.) surface
   as decode errors — silent drop would hide real broker misconfiguration.

5. **Pre-flight probe consistency.** `cli/verify.go` no longer checks the
   hard-coded legacy relation. It probes the same pair of relations the
   broker client tries (`RelCanIDeployToEnvironment` and
   `RelCanIDeployGeneric`) and only fails when both are absent.

## Why both shapes, and not just the modern one

Self-hosted Pact Broker forks ship behind by several releases. Users on
those forks would see c2quay regress if we dropped the legacy relations.
The fallback path is cheap (one `HasRelation` check) and keeps the
two-stage navigation opt-in via the absence of the root-level relation,
so modern brokers never pay for the fallback. Tests cover both.

## Consequences

- **Good:** c2quay works against official Pact Broker, PactFlow, and
  older forks without configuration.
- **Good:** `curies` arrays no longer break startup. Future broker
  features that emit array-valued relations degrade gracefully.
- **Good:** `record-deployment` POST bodies are now valid against strict
  broker validation; the pre-v0.4.5 body shape silently worked but would
  have failed against stricter broker releases.
- **Trade-off:** the broker client is slightly more code. The alternative
  — documenting "upgrade your broker" as a prerequisite — was rejected
  because c2quay's job is to work against what teams have deployed.
- **Trade-off:** two-stage record-deployment costs one extra GET per
  deploy. Negligible next to compose up and smoke tests. Log evidence
  makes the traversal auditable.
- **Not addressed here:** `pb:can-i-deploy-pacticipant-version-to-tag`.
  We never consumed tag-scoped gating (ADR 0001 implicitly prefers
  environment scope via `record-deployment` environments). If tag scope
  becomes desirable, a future ADR can layer it in.
