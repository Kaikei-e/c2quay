# ADR 0011 — `--force-recreate` is a debug escape hatch, not a policy

Date: 2026-04-17
Status: Accepted

## Context

`docker compose up -d` recreates a container when the resolved image's
digest differs from the digest of the image the running container was
created from. That is normally enough: if you rebuild locally or pull a new
image, the next `up` picks up the change. The 2026-04-17 incident hit a
case where it was not enough:

- Operator rebuilt images locally with the same tag (`app:local`).
- c2quay ran `docker compose up -d --remove-orphans --wait <services>`.
- Compose's digest diff evaluated against the *services in the plan*
  (c2quay always passes an explicit service list) and, for reasons that
  look like an interaction between `pull_policy: missing` and the
  service-scoped diff, skipped container recreation for some services.
- Old binaries kept running; manual `docker compose up -d --no-deps
  --force-recreate <svc>` was needed.

Whatever the exact cause, the operational question is: when Compose's
default diff misfires, what is the least-ceremony way to force a clean
recreate of the services we're shipping, without making every deploy churn
healthy containers?

## Decision

1. **Default behaviour is unchanged.** c2quay continues to run
   `docker compose up -d --remove-orphans [--wait]`. We trust Compose's
   digest diff for the common case. Always passing `--force-recreate`
   would replace healthy containers on every deploy, turning a
   zero-cost no-op into a recreation storm and papering over the
   occasional misfire (exactly what we want operators to *notice*, not
   mask).

2. **Per-deploy CLI flag.** `c2quay deploy --force-recreate` adds
   `--force-recreate` to the single `compose up` call of that deploy. No
   config key. A config-level always-on would turn a debug switch into
   policy, and the point of this ADR is that it must stay a switch.

3. **Warn-on-use, for the audit log.** `ShellAdapter.Up` emits a
   `slog.Warn` record when `UpOptions.ForceRecreate` is true. With
   `--audit-log` configured (already wired), the warning lands in the
   JSONL audit file, so operators can see in retrospect which deploys
   used the flag.

4. **Scope is the services in the plan.** The plan's service list is
   already explicit, so `--force-recreate` recreates exactly those
   services and no others. We do not attempt `--no-deps` coupling: if
   the operator set `--service` to narrow the deploy, only that service
   is in the plan and `--force-recreate` acts on it alone.

5. **Not a fix, a rescue.** The ADR documents that this flag should be
   used only when there is evidence the digest diff misfired — fresh
   build with a reused tag, suspected cache corruption, a known Compose
   regression. Habitual use is discouraged, and the warn log is how a
   reviewer catches that habit.

## Consequences

- **Good:** the 2026-04-17 recovery path (`docker compose up -d
  --force-recreate <svc>`) is now available from inside
  `c2quay deploy`, so the deploy stays under the gate + record path.
- **Good:** the default stays cheap; healthy containers are not churned.
- **Good:** audit-log coverage of the flag's use makes the "just pass it
  every time" anti-pattern visible to reviewers.
- **Trade-off:** a flag invites cargo-culting. The warn log is the
  mitigation; the runbook at
  [`../runbooks/tag-reuse-recreate.md`](../runbooks/tag-reuse-recreate.md)
  calls out the root-cause questions an operator should ask before
  reaching for it.
- **Not addressed here:** detecting "digest diff misfired" automatically
  and recreating only when it happened. That would require comparing
  `docker compose images` before/after and is a future ADR if the misfire
  class proves recurrent.
