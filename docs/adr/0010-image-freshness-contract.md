# ADR 0010 — Image freshness is the operator's contract, with an opt-in pull hook

Date: 2026-04-17
Status: Accepted
Extends: [ADR 0003](0003-compose-cli-source-of-truth.md)

## Context

`c2quay deploy` shells out to `docker compose up -d [--remove-orphans] [--wait]`
after the gate passes. That command does not build images, and it only pulls
remote images when Compose's own logic decides it should (the `pull_policy`
default is `missing`). Two operator workflows have been tripping on this:

1. **Local build-and-ship.** Operator commits code, runs `c2quay deploy`, and
   expects the new binary to go out. It does not: the image tag in
   `compose.yaml` already existed locally, `docker compose up` reuses it, and
   the container stays on the old code. This is precisely the 2026-04-17
   incident where `alt-backend` and `recap-worker` kept serving stale
   binaries for 30 minutes after the commit landed.

2. **Registry-fed rollout.** Operator's CI pushes a new tag to the registry,
   then another machine runs `c2quay deploy`. Compose's default
   `pull_policy: missing` will not fetch the new image because the old one is
   still in the local cache, so Compose has nothing to diff against and
   skips recreation.

Both failure modes share a root cause: c2quay does not enforce that the
artifact named by `compose.yaml` is actually the artifact the operator
intends to ship by the time the pipeline reaches `up`.

## Decision

1. **c2quay stays build-free.** Per ADR 0003, Compose is the source of truth
   for service definitions — build contexts, build args, bake overlays, and
   BuildKit caches all live in Compose. A `build` step inside c2quay would
   have to re-interpret all of that, which is the failure mode ADR 0003
   exists to prevent. The operator (or CI) runs `docker compose build` (or a
   Make target, or a registry push) **before** `c2quay deploy`. The runbook
   at [`../runbooks/image-freshness.md`](../runbooks/image-freshness.md)
   documents the two shapes.

2. **`deploy.pull` config key, default `never`.** A new optional field
   in `c2quay.yml`:

   ```yaml
   deploy:
     pull: never   # default; preserves v0.4 behaviour
     # pull: always   # run `docker compose pull <services>` before up
     # pull: missing  # let Compose's own default fire
   ```

   When set to `always`, `release.Deploy` inserts a step (2a) between the
   gate and `compose up` that runs `docker compose pull <services>` via the
   existing `ShellAdapter`. The pull step is scoped to the services in the
   plan — the same explicit service list that reaches `up`. A pull failure
   aborts the deploy before `up` is attempted; auto-rollback does not fire
   because nothing has changed in the running environment yet.

3. **`missing` is a no-op in c2quay.** Compose's own `up` already treats
   `missing` as the default; we surface the value for config symmetry but
   do not emit a separate `pull` call. Documented in the ADR so future
   readers don't wonder why only `always` takes the code path.

4. **Asymmetry with build is deliberate.** `pull` is "fetch what
   `compose.yaml` already names"; `build` is "compile something new". The
   first is a thin orchestration call; the second requires us to understand
   what Compose understands. We do not expose a `build` policy now, and the
   runbook tells operators how to handle it outside of c2quay.

## Consequences

- **Good:** the registry-fed-rollout workflow has a one-line fix
  (`deploy.pull: always`) that does not require operators to script a
  pre-step.
- **Good:** the build-and-ship workflow gets a runbook-level answer
  (`docker compose build` before `c2quay deploy`) and a clear reason that
  c2quay does not do it for them.
- **Good:** default is `never`, so every existing `c2quay.yml` keeps its
  current behaviour without change.
- **Trade-off:** operators who expected c2quay to DWIM for source-built
  images get a deliberate "no". If repeated reports show this is the more
  common shape, revisit — but do not add a `build` policy in this ADR.
- **Trade-off:** a `pull` step touches the network. Registry-auth failures
  are surfaced through `docker compose pull`'s own error output via
  `RunWithStream`.
- **Not addressed here:** a `--pull` CLI flag to override the config
  per-invocation. Skipped on purpose: `deploy.pull` is a reproducibility
  property of the environment, not a per-run choice. Add a flag in a later
  ADR if a concrete need appears.
