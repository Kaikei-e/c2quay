# Runbook — Image freshness before `c2quay deploy`

**When this applies:** any deploy where the binary/asset you expect to ship
is the *new* one, not whatever was already in the local Docker image cache.
If your deploy succeeded but the container is running an old binary, you
are probably in this runbook.

Context: see [ADR 0010](../adr/0010-image-freshness-contract.md). c2quay
does not build images, and by default it does not pull them either. The
operator (or CI) is responsible for making sure the image reference in
`compose.yaml` resolves to the artifact they want to ship by the time
`c2quay deploy` runs.

## Decision tree

### (a) You build images locally from source

Run the build **before** `c2quay deploy`:

```bash
docker compose build <services>
c2quay deploy --env production
```

In CI, the same shape as two steps in sequence:

```yaml
- run: docker compose build --pull
- run: c2quay deploy --env production
```

Leave `deploy.pull: never` (the default). c2quay does not need to fetch
anything — the freshly-built image is already in the local cache and
Compose's default digest diff will recreate the container.

### (b) You pull images from a registry

Set the pull policy in `c2quay.yml`:

```yaml
deploy:
  pull: always
```

c2quay will run `docker compose pull <services>` between the gate and
`compose up`, scoped to the services in the plan. A pull failure (auth,
transport, missing tag) aborts the deploy before anything changes in the
running environment.

### (c) Hybrid: some services are local, some are registry

`deploy.pull: always` is safe in this case. `docker compose pull` is a
no-op for services that only declare `build:` and no registry `image:`,
and fetches the rest. Run the local build first as in (a).

## How to tell which shape you are in

Run `docker compose config --format json` (what c2quay does internally)
and inspect `services.<name>.image`. If the reference looks like
`ghcr.io/...:tag` or `registry.example.com/...:tag`, you are in (b). If
it looks like `myapp:local` or similar with no registry host, and the
compose file has a `build:` section, you are in (a).

## What `deploy.pull` does *not* do

- **Does not build images.** By design. If you want c2quay to build, add
  your own pre-step; do not wait for a config knob — see ADR 0010.
- **Does not retag.** If your registry has a newer image under the same
  tag and your local cache also has that tag (stale), `pull` fetches the
  new digest and Compose's diff picks it up on `up`.
- **Does not save you from `latest`.** An immutable tag per release (a
  git SHA, a build timestamp) is still the right hygiene. `pull` fixes
  freshness; it does not fix reproducibility.

## If you are stuck now (the 2026-04-17 shape)

Symptoms: deploy succeeded, broker recorded the new version, but the
container is running the old binary.

Remediation (manual):

```bash
docker compose build <affected-services>
docker compose up -d --no-deps --force-recreate <affected-services>
```

Structural fix: adopt (a) or (b) above, so the next deploy does not need
the manual steps. Consider also enabling
[ADR 0011's](../adr/0011-force-recreate-escape-hatch.md)
`--force-recreate` flag for the one-off recovery deploy if you want to
keep the audit trail inside c2quay.
