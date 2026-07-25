# c2quay

[![ci](https://github.com/Kaikei-e/c2quay/actions/workflows/ci.yml/badge.svg)](https://github.com/Kaikei-e/c2quay/actions/workflows/ci.yml)

**Contract-gated releases for Docker Compose.**

> ⚠️ **Status: v0.3.0 — alpha.** The CLI surface (`doctor`, `verify`, `deploy`, `status`) is functional and test-covered. Feedback and bug reports welcome via Issues.

> Design references: [docs/architecture.md](docs/architecture.md), [docs/adr/](docs/adr/).

c2quay is a small CLI that sits between your Pact Broker and your Docker Compose deployments. It refuses to let a release go out if consumer-driven contracts say it would break something — and records the deployment back to the broker when it succeeds.

```bash
c2quay deploy --env production
```

```
✓ Resolving versions for production (api@a1b2c3d, worker@e4f5g6h)
✓ can-i-deploy: api@a1b2c3d → production (safe)
✓ can-i-deploy: worker@e4f5g6h → production (safe)
→ docker compose up -d api worker
✓ Smoke check passed
✓ record-deployment posted to broker
```

## The problem

If you deploy with Docker Compose, you probably already know this shape of outage:

1. `api` ships a breaking change to a response shape
2. `worker` — which consumes that response — is still on the old version
3. `docker compose up -d` happily rolls out `api`
4. `worker` starts throwing deserialization errors in production

Pact's `can-i-deploy` was built exactly for this. But in the Compose world, wiring it into the deployment path is a hand-rolled shell script every time: fetch the version, call the broker, parse the output, decide whether to proceed, remember to call `record-deployment` afterwards. Most teams skip it, or implement half of it.

c2quay is that script, hardened into a tool.

## Scope

**What c2quay does**

- Resolves which image versions a deploy will produce
- Gates the deploy on `can-i-deploy` from your Pact Broker
- Runs the actual rollout via `docker compose`
- Posts `record-deployment` back to the broker on success

**What c2quay deliberately doesn't do**

- Replace Compose. Images, ports, health checks, dependencies — all of that stays in your `compose.yaml`.
- Replace an orchestrator. No scheduling, no mesh, no autoscaling. If you need Kubernetes, use Kubernetes.
- Guarantee zero downtime. Contract compatibility is not the same as migration safety or runtime health. c2quay tells you the contracts are compatible; the rest is still your responsibility.

## Is this for you?

c2quay is aimed at a specific band of teams. You're probably in it if:

- you deploy with Docker Compose on one host or a handful of hosts
- you already use Pact for consumer-driven contract testing
- you want release gating without adopting a full platform

You're probably *not* in it if you're already on Kubernetes, ECS, or Nomad — those ecosystems have their own gating stories, and c2quay has nothing to offer over them.

## Where c2quay sits among similar tools

| | **c2quay** | Docker Compose | Kamal | Kubernetes |
|---|---|---|---|---|
| Primary concern | Release gating | Service definition | Zero-downtime deploys | Orchestration |
| Compose-native | ✓ | ✓ | ✗ (own format) | ✗ |
| Pact gate built in | ✓ | ✗ | ✗ | ✗ |
| Target scale | 1–few hosts | 1–few hosts | 1–few hosts | Clusters |

The closest tool in spirit is **Kamal**, and the difference is worth being explicit about. Kamal is a deployment tool that happens to work well for small fleets; its central concern is zero-downtime rollout with its own config model. c2quay is a *gating* tool that happens to drive Compose; its central concern is refusing unsafe releases. The two could in principle coexist — you could run `c2quay verify` before `kamal deploy` — but c2quay is built for teams who want to stay on Compose rather than adopt a new deployment DSL.

## Planned CLI

These commands define the intended surface. Implementation is in progress; see the roadmap below.

### `c2quay verify`

Check whether a deploy *would* be safe, without doing anything.

```bash
c2quay verify --env production
c2quay verify --env production --service api
c2quay verify --env production --output json
```

### `c2quay deploy`

Run the gated deployment: verify → compose up → record.

```bash
c2quay deploy --env production
c2quay deploy --env production --service api
c2quay deploy --env production --dry-run
c2quay deploy --env production --auto-rollback=off       # keep current images on failure
c2quay deploy --env production --auto-rollback=dry-run   # preview the rollback plan
c2quay deploy --env production --force-recreate          # ADR 0011 escape hatch
```

Auto-rollback is **on by default**. When `docker compose up` or the smoke
check fails, c2quay writes a compose override pinning each service back to
the image it had in the pre-deploy snapshot and re-runs `compose up --wait`.
`record-deployment` is never called during auto-rollback, so the broker's
view of the previous version stays correct. Rollback is intentionally **not**
triggered when `record-deployment` itself fails — compose has already
succeeded at that point, and which side to reconcile (re-post to the broker
vs. roll compose back by hand) is an operator decision.

### `c2quay rollback`

Manually restore a previous snapshot's images. Useful when auto-rollback was
disabled, when `record-deployment` failed, or when restoring from an older
snapshot than the most recent one.

```bash
c2quay rollback --env production --from-snapshot .c2quay/snapshots/20260417T120000Z-pre.json
c2quay rollback --env production --from-snapshot <file> --dry-run
```

### `c2quay status`

Show what's currently deployed per environment, per pacticipant.

```bash
c2quay status --env production
```

## Configuration

```yaml
compose:
  files:
    - compose.yaml
  project_name: myapp

broker:
  base_url: https://pact-broker.example.com
  # Auth lives in env vars, never in this file:
  #   PACT_BROKER_USERNAME / PACT_BROKER_PASSWORD  (Basic)
  #   PACT_BROKER_TOKEN                            (Bearer)

versioning:
  strategy: manifest_file   # or: resolved_image_digest | git_sha
  options:
    path: .c2quay/versions.json
    # For the git_sha strategy only:
    #   short: true    # `git rev-parse --short HEAD` (core.abbrev length)
    #   short: "12"    # `git rev-parse --short=12 HEAD`

deploy:
  wait: true
  wait_timeout: 180s
  pull: never   # never | always | missing — see ADR 0010
  smoke:
    command: ./scripts/smoke.sh
    timeout: 30s
    env:
      TARGET_ENV: production

environments:
  production:
    all_or_nothing: true
    services:
      api:
        pacticipant: api
      worker:
        pacticipant: worker
```

**`all_or_nothing`.** When `true` (recommended for monolithic rollouts
where every service ships at the same version), `verify` and `deploy`
send the whole candidate set to the broker's matrix endpoint in a single
`can-i-deploy` request. The broker evaluates the services together, so a
release that updates N consumers and their provider simultaneously is not
gated on the (nonexistent) pact between the new consumer and the *current
prod* provider. Works against current Pact Broker (`pb:matrix`), older
forks (`pb:can-i-deploy`), and modern brokers that only advertise the
scope-specific relation — in the last case c2quay constructs
`${broker_base}/matrix` directly. When `false` (default), c2quay falls
back to per-service `can-i-deploy` calls, which is the right shape for
independently-versioned services. See
[ADR 0009](docs/adr/0009-aggregate-can-i-deploy-for-all-or-nothing.md)
for the background incident.

A worked example lives in [`docs/config.example.yml`](docs/config.example.yml).

**`gate_only`.** Set `gate_only: true` on a service mapping when that
service is not declared in this box's `compose.yaml` — typically because it
runs on a separate host (a remote GPU worker, a managed dependency you
don't orchestrate with Compose here). c2quay still gates it (can-i-deploy /
aggregate matrix) and records its deployment to the broker, but never
passes it to `docker compose up`, `pull`, `--force-recreate`, or a
rollback. Before the gate runs, c2quay resolves the actual Compose service
list (`docker compose config --services`) and fails fast if a *non*-
`gate_only` mapped service is missing from it — without this check, a
missing mapping fails the whole `docker compose up` call after every other
service has already cleared the Pact gate, because Compose validates its
positional service arguments before starting anything. It also warns (does
not fail) if a `gate_only` service unexpectedly *does* exist in Compose —
that usually means the mapping was never flipped back after the service
moved into `compose.yaml`. See
[ADR 0013](docs/adr/0013-gate-only-services.md).

**`deploy.pull`.** c2quay does not build images and by default does not
pull them either — the operator (or CI) is responsible for image
freshness before `c2quay deploy` runs. Set `deploy.pull: always` to run
`docker compose pull <services>` between the gate and `compose up`;
this fits the registry-fed rollout shape. For locally-built images,
run `docker compose build` before `c2quay deploy`. See
[ADR 0010](docs/adr/0010-image-freshness-contract.md) for the decision
and [runbook](docs/runbooks/image-freshness.md) for the workflows.

**`--force-recreate`.** `c2quay deploy --force-recreate` passes the
flag through to `docker compose up` for the current deploy only. This
is a debug escape hatch for the fresh-build-same-tag case where
Compose's digest diff misfires — habitual use is discouraged and the
adapter emits a `slog.Warn` when the flag fires, so the audit log
shows which deploys used it. See
[ADR 0011](docs/adr/0011-force-recreate-escape-hatch.md) and the
[tag-reuse runbook](docs/runbooks/tag-reuse-recreate.md).

The split is deliberate: Compose owns *what the service is*, c2quay owns *how the release happens*. c2quay never touches the service definition.

## Requirements

- Go 1.25 or later (development baseline 1.26.2).
- Docker Compose **v2.40.2+** (CVE-2025-62725 fix). The hyphenated
  `docker-compose` v1 is not supported.
- A Pact Broker at v2.113 or newer, or PactFlow.

## Roadmap

- [x] Project scaffolding (`doctor`, `c2quay.yml`, slog audit log)
- [x] Config loader and schema
- [x] Compose adapter (shell-out, `--wait` ps cross-check)
- [x] Versioning strategies (`manifest_file`, `resolved_image_digest`, `git_sha`)
- [x] HAL-driven Pact Broker client
- [x] `verify` command
- [x] `deploy` command (local, `--dry-run`, rollback hints)
- [x] `record-deployment` posting
- [x] `status` command
- [x] Smoke check hook
- [ ] `deploy` over SSH
- [x] Automatic rollback execution (opt-out via `--auto-rollback=off`)

> **Upgrading from v0.3:** auto-rollback is on by default. If your workflow
> relied on the old "fix and retry" behaviour (compose left in the failed
> state so an operator can inspect it before reverting), pass
> `--auto-rollback=off` on the `c2quay deploy` call.

## Contributing

The project is pre-alpha and the design is still moving. The most useful contributions right now are:

- **Use-case reports.** If you've built the "Compose + Pact" combo in-house, what did yours do? What went wrong? Open an issue.
- **Design pushback.** The CLI surface and config shape above are proposals, not commitments.
- **Prior art pointers.** If there's an existing tool that already does this well, I want to know before writing more code.

```bash
git clone https://github.com/Kaikei-e/c2quay.git
cd c2quay
make build
make test            # unit
make e2e             # end-to-end (builds the binary and drives it)
```

## License

Apache License 2.0. See [LICENSE](./LICENSE).
