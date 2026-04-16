# ADR 0004 — `record-deployment` is always the last step

Date: 2026-04-17
Status: Accepted

## Context

Pact Broker's `record-deployment` endpoint, when called, marks the given
pacticipant version as deployed to the environment — and automatically
un-deploys any previous version. That is the *point* of the endpoint, and
it lets the next `can-i-deploy` ask make correct decisions.

A naive deploy script might call `record-deployment` early ("before anyone
forgets") or in parallel with the compose up. Both are wrong: if the deploy
fails afterwards, the broker records a deployment that never happened, and
the previous version's status is already wiped.

## Decision

In c2quay, `record-deployment` is called in step (f) of the deploy
pipeline, after:

- (a) lock acquired
- (b) pre-snapshot captured
- (c) gate check passed
- (d) `docker compose up` succeeded (with `--wait` cross-check)
- (e) optional smoke passed

If any of (a)–(e) fails, `record-deployment` is not called — full stop.
The rollback hint explicitly calls out that the broker still records the
previous version.

## Consequences

- Correctness of `can-i-deploy` is preserved across failed deploys.
- Operators get a deterministic "the broker says X is deployed" view.
- Very late-stage failures (e.g., broker down at record time, after compose
  up already succeeded) leave the cluster in a state where the broker's
  view lags reality. The rollback hint alerts the operator to manually
  reconcile.
