# ADR 0006 — Automatic rollback is Compose-side only

Date: 2026-04-17
Status: Accepted

## Context

README roadmap line 167 promised "Automatic rollback execution" for v0.3.
Through v0.3.0 the pipeline only emitted a human-readable hint on failure
(`internal/release/rollback_hint.go`); operators had to revert their
`compose.yaml` and re-run `docker compose up` by hand. That is fine for
small teams but contradicts the roadmap, and several common failure modes
(a transient `--wait` timeout, a flaky smoke check) could reliably be
recovered from without human intervention.

Two design forces collided:

- **ADR 0004**: `record-deployment` is always the last step. A deploy that
  fails at any earlier step leaves the broker's view of "what's deployed"
  *unchanged*. Therefore, rolling the Compose side back to the pre-deploy
  state leaves the whole system consistent, with no broker call required.
- **ADR 0001**: Release identity is immutable. We can rely on the image
  references captured at pre-snapshot time being valid to re-apply later.

## Decision

1. **Compose-side only.** Auto-rollback re-applies the pre-deploy image map
   via a generated compose override (`services.<name>.image: <prev_ref>`)
   and runs `docker compose up -d --wait`. It never calls
   `record-deployment` and never writes to the broker.

2. **Opt-out, not opt-in.** `--auto-rollback` defaults to `on`. The roadmap
   said "automatic"; silent-by-default behaviour that requires a flag to
   activate would be surprising. Operators can still pin the old behaviour
   by passing `--auto-rollback=off`.

3. **Policy:** auto-rollback runs for failures at `compose-up` and
   `smoke`, and is a no-op for every other step.

   | Failed step       | Auto-rollback? | Why                                              |
   |-------------------|----------------|--------------------------------------------------|
   | lock / pre-snap   | no             | nothing changed                                  |
   | gate              | no             | no state changed upstream                        |
   | **compose-up**    | **yes**        | partial Compose state; restore                   |
   | **smoke**         | **yes**        | containers up but unhealthy; restore             |
   | record-deployment | **no**         | compose healthy on new version; operator-only    |
   | post-snapshot     | no             | deploy actually succeeded                        |

4. **Why not auto-rollback on `record-deployment` failure?** At that point
   compose is healthy and serving the new version. The only thing "wrong"
   is that the broker hasn't been told. Automatically reverting a running,
   healthy deployment because a broker call failed would be the opposite
   of safe. The right response is either re-post to the broker, or decide
   to roll back — and only the operator knows which.

5. **Fresh context for rollback.** The parent deploy context may already
   be exhausted (a `--wait` timeout at compose up is a common trigger for
   rollback). The executor derives a new `context.Background()` with its
   own timeout (defaults to 2× `deploy.wait_timeout`, or 3 min). Parent
   cancellation (`SIGINT`) still propagates via a goroutine watcher.

6. **Audit trail.** Every rollback writes:
   - `.c2quay/rollback/<ts>-override.yml` (the generated override)
   - `.c2quay/snapshots/<ts>-rollback.json` (post-rollback ps+images)
   - `.c2quay/rollbacks/<ts>.json` (structured RollbackReport: mode,
     succeeded, services, duration, err)

   The `RollbackHint` appended to stderr links to these files.

7. **Manual mode.** For the `record-deployment`-failure case above, and
   for recoveries that span multiple deploys, `c2quay rollback
   --from-snapshot <file>` drives the same executor against any stored
   snapshot.

## Consequences

- **Good:** common transient failures heal themselves without paging
  anyone. The audit trail is preserved. ADR 0004's invariant still holds:
  the broker is never told about a deploy that didn't happen.
- **Good:** predictable — the same pre-deploy images are what's running
  after rollback, and the broker already matches that.
- **Trade-off:** opt-out is a minor behaviour change from v0.3.0.
  Documented in the README upgrade note; an `--auto-rollback=off` flag is
  always available.
- **Trade-off:** rollback requires `docker compose config --format json`
  at pre-snapshot time. If that call fails (unusual), rollback is skipped
  with a logged reason — never silently wrong.
- **Not addressed here:** rolling back across reboots, partial rollback
  of a subset of services, rollback on SSH-remote deploys. Future ADRs.
