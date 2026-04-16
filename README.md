# c2quay

**Contract-gated releases for Docker Compose.**

> ⚠️ **Status: Design stage.** Architecture and CLI surface are being designed in the open. Not ready for use. Feedback and discussion welcome via Issues.

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
```

### `c2quay status`

Show what's currently deployed per environment, per pacticipant.

```bash
c2quay status --env production
```

## Planned configuration

```yaml
compose:
  files:
    - compose.yaml
    - compose.prod.yaml
  project_name: myapp

pact_broker:
  url: https://broker.example.com

versioning:
  strategy: git_sha   # or: image_tag, file

environments:
  production:
    hosts:
      - deploy@prod.example.com
    services:
      api:
        pacticipant: api
      worker:
        pacticipant: worker

release:
  smoke:
    command: ./scripts/smoke.sh production
```

The split is deliberate: Compose owns *what the service is*, c2quay owns *how the release happens*. c2quay never touches the service definition.

## Roadmap

Rough ordering. Items will likely shift as the design meets reality.

- [x] Project scaffolding
- [ ] Config loader and schema
- [ ] `verify` command (Pact Broker `can-i-deploy` integration)
- [ ] `deploy` command (local Compose execution)
- [ ] `deploy` over SSH
- [ ] `record-deployment` posting
- [ ] `status` command
- [ ] Smoke check hook
- [ ] Rollback
- [ ] Release history

## Contributing

The project is pre-alpha and the design is still moving. The most useful contributions right now are:

- **Use-case reports.** If you've built the "Compose + Pact" combo in-house, what did yours do? What went wrong? Open an issue.
- **Design pushback.** The CLI surface and config shape above are proposals, not commitments.
- **Prior art pointers.** If there's an existing tool that already does this well, I want to know before writing more code.

```bash
git clone https://github.com/TODO/c2quay.git
cd c2quay
go build -o c2quay ./cmd/c2quay
go test ./...
```

## License

Apache License 2.0. See [LICENSE](./LICENSE).
