# ADR 0013 — `gate_only` services: gated and recorded, never handed to Compose

Date: 2026-07-25
Status: Accepted

## Context

A consumer of c2quay (the Alt monorepo) maps `environments.<env>.services`
to `{pacticipant}` for every service whose contracts should gate a
production rollout. One of those mapped services, `tts-speaker`, runs on a
separate GPU host and is intentionally **not** declared in
`compose/compose.yaml` on the box c2quay drives — there is nothing for
Compose to start. The broker still needs to know `tts-speaker`'s version is
part of the release, because another service's consumer-side pact against
it must resolve to something other than "no verification result" during
the aggregate can-i-deploy check.

c2quay had no concept of "in the gate, not in Compose":

- `release.BuildPlan` (`internal/release/gate.go`) puts every mapped
  service into `plan.Services` with no distinction.
- `release.Deploy` (`internal/release/deploy.go`) passes `plan.Services`
  verbatim as positional arguments to `docker compose up`, `pull`, and
  (via the rollback flow) `--force-recreate`.
- `docker compose` validates every positional service name **before**
  starting anything. A single unresolvable name — `tts-speaker` — fails
  the whole invocation with `no such service: tts-speaker`.

The result: production deploys hard-failed at the compose-up step, after
every mapped service (including `tts-speaker`) had already cleared the
Pact gate. The gate passing gave no signal that the deploy was about to
fail — the failure mode lived entirely in a step c2quay had no visibility
into ahead of time, and it took down the ENTIRE batch, not just the one
service that doesn't belong to this box.

## Decision

1. **`gate_only: bool` on `ServiceMapping`.** A service mapped with
   `gate_only: true` is:
   - **Included** in the Pact gate — per-service `can-i-deploy` fan-out and
     the `all_or_nothing` aggregate matrix query both cover it, exactly
     like any other mapped service. Its pacticipant version must still be
     verified.
   - **Included** in `record-deployment` — the broker's view of "what's
     running in this environment" must include it, because it genuinely is
     running, just not under this box's Compose.
   - **Excluded** from every `docker compose` operation: `up`, `pull`,
     force-recreate, and snapshot/rollback. c2quay never expects to find it
     in `docker compose config`.

2. **`Plan` exposes two service lists.** `release.Plan.Services` keeps its
   existing meaning — every mapped service, gate_only included — so gating
   and recording are unchanged. A new `Plan.DeployServices` field holds the
   subset that should actually reach Compose. For configs without
   `gate_only`, `DeployServices` always equals `Services`: existing
   behaviour is preserved byte-for-byte. `Deploy` was updated to pass
   `DeployServices` (not `Services`) to `Compose.Pull` and `Compose.Up`.

3. **Empty `DeployServices` skips Compose entirely, it does not call `up`
   with zero services.** `docker compose up -d` with no positional service
   arguments means "bring up every service in the project" — the opposite
   of "there is nothing to deploy here." If every service in scope for a
   given deploy (e.g. `--service tts-speaker` on its own) is `gate_only`,
   `Deploy` skips the Pull/Up block and logs/warns instead.

4. **Plan-time compose coverage validation, before the gate runs.** Right
   after `BuildPlan` and before the pre-deploy snapshot or any broker call,
   `release.ValidateComposeCoverage` resolves the actual Compose service
   list via a new `ComposeDeployer.ConfigServices` method
   (`docker compose config --services`) and:
   - **Errors** if any *non*-`gate_only` mapped service is missing from
     Compose: `service "X" is mapped in environments.<env> but does not
     exist in the compose config; if it is deployed outside compose, mark
     it gate_only: true`. This is the fail-fast replacement for the
     `no such service` crash — the operator sees a clear, actionable
     message before the gate runs at all, not a Compose stack trace after
     it passes.
   - **Warns** (does not fail) if a `gate_only` service unexpectedly *does*
     exist in Compose — most likely the service moved into `compose.yaml`
     and the mapping was never flipped back. Still excluded from Compose
     operations regardless; the warning is purely for the operator to
     notice and clean up the mapping.
   - Runs before the pre-deploy snapshot, so a bad mapping never touches
     Compose or the broker at all.

5. **No silent fallback when Compose itself is unreachable — except
   `--dry-run`.** If the `docker compose config --services` call itself
   fails (Docker not installed, daemon down, bad compose file), that is a
   hard error for a real deploy: c2quay does not silently skip the check it
   just added to prevent hard failures. The one exception is `--dry-run`:
   an operator planning from a box without a live Compose install should
   still see the gate check run, so a `ConfigServices` failure in
   `--dry-run` downgrades to a loud `UI.Warn` and validation is skipped for
   that run only. `--dry-run` without `gate_only` in play behaves exactly
   as before.

6. **Rollback needed no code changes.** `BuildRollbackPlan` derives its
   service list from a snapshot's `Images` map, which is populated from
   `docker compose config`'s rendered services
   (`RenderedConfig.ImagesByService`). A `gate_only` service was never
   representable there to begin with — it is absent from Compose by
   definition — so pre-deploy snapshots, diffs, and rollback plans already
   ignore it without any special-casing. This ADR adds a regression test
   pinning that invariant down explicitly, but changes no rollback code.

## Consequences

- **Good:** the incident class this ADR exists to prevent — a whole-batch
  `no such service` hard failure discovered only after the gate has
  already passed — can no longer happen for a correctly-flagged
  `gate_only` mapping, and is caught with an actionable message *before*
  the gate runs for an incorrectly-flagged one.
- **Good:** `gate_only` services stay first-class from the broker's point
  of view — gated, recorded, versioned with the same `git_sha` (or other
  strategy) stamp as everything else in the release. Nothing about
  contract safety is weakened.
- **Good:** zero behavioural change for configs that don't use
  `gate_only` — `DeployServices == Services`, validation only ever warns or
  no-ops when every mapped service is actually in Compose.
- **Trade-off:** `ComposeDeployer` gained a fourth method
  (`ConfigServices`), so every fake implementing it in tests needed a stub.
  This is the standard cost of extending a narrow, ISP-shaped interface and
  was mechanical to apply.
- **Trade-off:** a `gate_only: true, exists in Compose` misconfiguration is
  a warning, not a hard error. We chose not to block the deploy on it
  because the state is self-correcting (the service is actually deployable
  through Compose, just not through the `DeployServices` path) and
  operators may be mid-migration (moving a service *into* Compose from a
  remote host). A future ADR could tighten this to an error if the warning
  proves to go unnoticed in practice.
- **Not addressed here:** validating that a `gate_only` service's declared
  pacticipant is actually reachable/healthy on its remote host. c2quay's
  contract-gating scope stops at the Pact Broker; runtime health of
  services it doesn't orchestrate is out of scope, same as it always has
  been for anything outside Compose.
