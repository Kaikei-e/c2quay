# ADR 0009 — Aggregate can-i-deploy for all_or_nothing rollouts

Date: 2026-04-17
Status: Accepted
Extends: [ADR 0008](0008-hal-scoped-relations-and-two-stage-record.md)

## Context

c2quay v0.4.x gated `verify` and `deploy` by fanning out to the broker with
one `can-i-deploy` call per service (`release/pipeline.go:GateAll` issuing
`broker.Client.CanIDeploy` against the scoped
`pb:can-i-deploy-pacticipant-version-to-environment` relation introduced by
ADR 0008). Each call asks the broker the same question in isolation:
*"is pacticipant P at version V safe to ship to environment E, given
whatever is currently deployed there?"*

That framing is correct for independent deploys but wrong for the
monolithic rollout we actually use in production. On 2026-04-17 we tried
to ship 13 services at the same `cd2c3499f` git SHA. The broker
(correctly, given the question c2quay asked) gated the release on acolyte:

> acolyte@cd2c3499f cannot be deployed to production because there is no
> verified pact against news-creator@1c7679801 (current prod version).

The verified pact that does exist is between `acolyte@cd2c3499f` and
`news-creator@cd2c3499f` — exactly the combination we were shipping.
Our pre-deploy `pact-check.sh` verifies the candidate set against itself,
not against current prod. Per-service can-i-deploy could never see that.

Pact Broker's matrix endpoint solves this: you POST/GET one request with
the *entire candidate set* as selectors, and the broker evaluates them
together. The `pact-broker can-i-deploy` CLI has done this for years via
repeated `--pacticipant X --version V` flags. c2quay had the
`config.Environment.AllOrNothing` field declared but never wired up.

## Decision

1. **`all_or_nothing: true` switches the gate to an aggregate matrix
   query.** When an environment has `all_or_nothing: true` and more than
   one service is in the plan, `release.GateAll` sends a single request to
   the matrix endpoint with every `(pacticipant, version)` pair as a
   selector. The broker's summary is the deploy-the-set verdict, and
   per-row verification results drive per-service reporting.

2. **Generic `pb:can-i-deploy` is required for aggregate mode.** The
   scoped `pb:can-i-deploy-pacticipant-version-to-environment` relation
   from ADR 0008 is single-pacticipant by construction and cannot carry a
   multi-selector query. `verify` and `deploy` now refuse to start when
   `all_or_nothing: true` but the broker index lacks
   `pb:can-i-deploy`. Silently falling back to the per-service path would
   restore the exact failure mode this ADR exists to fix.

3. **No verification record ⇒ gated.** If a selector appears in the
   candidate set but the matrix response contains no row naming it as
   consumer or provider, c2quay reports `Deployable=false` with
   `Reason: "no verification record between <pacticipant>@<version> and
   its integration partners"`. This is stricter than trusting
   `summary.deployable` alone, because operators still need to know which
   candidate has unverified integrations.

4. **`all_or_nothing: false` keeps the per-service fan-out.** Default
   behaviour is unchanged: four-way parallel `CanIDeploy` calls against
   whichever can-i-deploy relation the broker exposes. This preserves the
   semantics teams with truly-independent services rely on.

5. **Query template support (ADR 0008 did not cover this).** Modern
   brokers advertise the matrix endpoint with an RFC 6570 level-3 query
   template (`/matrix{?pacticipant,version,environment,latestby}`).
   `Link.ExpandTemplate` is level-1 only. Rather than generalise template
   expansion, the aggregate path strips the `{?...}` tail and constructs
   the query directly from `url.Values` — we always know what parameters
   we want, and the template is advisory anyway.

## Query shape

```
GET /matrix?\
  q[][pacticipant]=acolyte&q[][version]=cd2c3499f&\
  q[][pacticipant]=news-creator&q[][version]=cd2c3499f&\
  ...&\
  latestby=cvp&environment=production
```

`q[]` index order pairs pacticipant and version. `latestby=cvp` narrows
rows to the latest consumer-version-provider combination (the selector
the `pact-broker` CLI uses internally for `can-i-deploy`). `environment`
constrains the matrix to rows that would be compared against what is
deployed to that environment.

## Per-pacticipant attribution

The matrix response returns rows shaped like:

```json
{
  "consumer": {"name": "acolyte", "version": {"number": "cd2c3499f"}},
  "provider": {"name": "news-creator", "version": {"number": "cd2c3499f"}},
  "verificationResult": {"success": true, "_links": {...}}
}
```

For each selector we collect every row in which it appears as consumer
or provider. A selector is `Deployable=true` only when every such row has
`verificationResult.success=true`. If no row names it, we report no
verification record; this catches the case where a pacticipant is in the
candidate set but has no integration partner the broker knows about.

## Consequences

- **Good:** the 2026-04-17 incident no longer gates a correct rollout. 13
  services shipped together at the same SHA evaluate as a set.
- **Good:** API-call count drops from N to 1 for aggregate gates. 13
  matrix rows is cheap compared to 13 round trips.
- **Good:** `all_or_nothing: false` is unchanged, so teams that chose
  per-service semantics keep them.
- **Trade-off:** aggregate mode requires the generic `pb:can-i-deploy`
  relation. ADR 0008 established that modern brokers expose it alongside
  the scoped relation, so in practice this affects only very old forks;
  those forks would not implement the matrix endpoint correctly anyway.
- **Trade-off:** aggregate responses are larger than per-service ones.
  13 services × integration partners is still well under 1 MB in
  practice; we read the full body before decoding, matching the existing
  client shape.
- **Not addressed here:** partial-rollout semantics under
  `all_or_nothing: false` plus multi-selector. If a team eventually wants
  "check these three services together but allow the rest to remain as
  deployed," we can add a separate selector list to the config and branch
  without revisiting this ADR.
