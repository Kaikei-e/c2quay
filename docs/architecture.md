# c2quay Architecture

c2quay is a single binary CLI that gates Docker Compose deployments on Pact
Broker's `can-i-deploy`. This document describes the runtime flow and the
invariants each package is responsible for.

## 1. Responsibility split

```
    cmd/c2quay  ──────────────────┐
                                  │
          internal/cli  ───────┐  │
                               ▼  ▼
                       internal/release
                       ┌──────┬──────┬──────┬──────┐
                       │ gate │ diff │smoke │rollback-hint│
                       ▼      ▼      ▼      ▼
   internal/broker    internal/composeadapter    internal/versioning
   (HAL driven)       (shell-out)                (manifest / digest / sha)

   internal/config   internal/lock   internal/doctor   internal/logging
```

Every arrow points downward; lower layers do not import upward.

## 2. The verify flow

1. `cli/verify.go` loads `c2quay.yml` and resolves `--env`.
2. `composeadapter.NewShell` is created but never runs anything for verify.
3. `versioning.Factory` picks the strategy. `resolved_image_digest` calls
   `config --format json` on the real Compose; the others don't.
4. `release.BuildPlan` pairs each service with its pacticipant + release.
5. `broker.New(...)` then `bc.Start(ctx)` fetches the HAL index.
6. `release.GateAll` fans out `can-i-deploy` calls with `errgroup` at
   parallelism 4.
7. Output writer emits text or JSON.

## 3. The deploy flow (strict ordering)

```
(a) file lock        ─ fail-closed if another process holds it
(b) pre snapshot     ─ ps + configured releases + resolved {service → image}
(c) gate check       ─ reuses verify pipeline
(c') pull (optional) ─ if deploy.pull == always, `docker compose pull <svcs>`
                       runs after the gate and before (d). Failure aborts the
                       deploy before anything mutates. See ADR 0010.
(d) compose up       ─ --remove-orphans --wait, with ps cross-check
(e) smoke (optional) ─ shell command with TARGET_ENV injection
(d')/(e') auto-rollback on (d)/(e) failure when --auto-rollback=on (default):
                       write pinned-image override, re-run `compose up --wait`,
                       capture post-rollback snapshot. Never re-invokes
                       `record-deployment`. Policy deliberately skips rollback
                       after (f) — see ADR 0006.
(f) record-deployment─ MUST be last; never earlier
(g) post snapshot
(h) lock release
```

The record-deployment invariant is non-negotiable. Calling it earlier would
cause the broker to auto-undeploy the previous version, and the next
`can-i-deploy` would make decisions on top of a deployment that never
actually happened.

## 4. Compose CLI expectations

- Minimum CLI: v2.40.2 (CVE-2025-62725 fix).
- `docker-compose` (hyphen form): refused. v1 was removed upstream in 2025-04.
- `up --wait` has a known false-positive bug
  ([docker/compose#10596](https://github.com/docker/compose/issues/10596));
  `composeadapter.ShellAdapter.Up` compensates by calling `ps --format json`
  after the up and masking the non-zero exit when every service is healthy.

## 5. HAL-driven broker client

`internal/broker/client.go` fetches the broker's index (`/`) on `Start()` and
caches `_links`. All subsequent operations look up their URL by relation
name (`pb:can-i-deploy`, `pb:record-deployment`, `pb:environments`). This
lets the broker evolve its routes without breaking c2quay, and it surfaces
"your broker is too old to support this feature" as a clear error instead of
a mysterious 404.

URL template expansion uses RFC 6570 level-1 (`{var}` only), which is all
the Pact Broker uses today.

## 6. Immutable release identity

c2quay refuses mutable release identifiers. See
[ADR 0001](adr/0001-immutable-release-identity.md) for the rationale.

## 7. Observability

- All major steps emit structured slog records via `slog.GroupAttrs`
  (grouped as `gate`, `broker`, `compose`).
- `--audit-log <path>` opens a JSON Lines file in 0600 mode and tees every
  slog record to it in addition to the human-readable stderr writer.
- `ShellAdapter.Up` logs a warning when masking the `--wait` false-positive,
  so operators can tell that the bug workaround fired.

## 8. What c2quay does not do

- Atomic deploys (see [plan §2.4](../c2quay_final_plan.md)). The goal is
  restart-safe and auditable, not transactional.
- Partial rollouts (`all_or_nothing: false` is reserved for future use).
- Broker-side rollback. Auto-rollback restores the Compose plane only;
  `record-deployment` is never called during rollback (which is correct: the
  broker still records the previous version, and that is what's running
  again). See [ADR 0006](adr/0006-auto-rollback.md) for the rationale.
- Auto-rollback after `record-deployment` failure. By the time that step can
  fail, compose is already healthy on the new version. Whether to re-post to
  the broker or roll compose back by hand is an operator decision, not an
  automatic one.
- SSH remote deploys. Future extension; not in v0.
