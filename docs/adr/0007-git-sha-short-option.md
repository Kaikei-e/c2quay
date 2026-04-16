# ADR 0007 — Allow abbreviated git SHAs in the `git_sha` strategy

Date: 2026-04-17
Status: Accepted

## Context

The `git_sha` strategy (see ADR 0001) resolves every service's release
identity to the output of `git rev-parse HEAD` — a 40-character hex string.

Operators running c2quay against real Pact Brokers hit two practical
problems with the full SHA:

- Some brokers, registries, and CI UIs truncate or render 40-char hashes
  awkwardly in tables and audit logs.
- Teams already tag images with short SHAs (`api:a1b2c3d`) because that is
  what their image build tooling produces. Forcing them to register the
  full 40-char form in the broker but tag with 7 chars creates a drift
  between what `can-i-deploy` sees and what is actually deployed, which is
  the exact failure mode ADR 0001 exists to prevent.

## Decision

Introduce `versioning.options.short` for the `git_sha` strategy. Accepted
values in YAML (the option map is `map[string]string`):

| YAML value      | Effect                                               |
|-----------------|------------------------------------------------------|
| *absent*        | Full 40-char SHA (v0.4.4 and earlier behaviour).     |
| `false` / `"0"` | Full SHA, explicit.                                  |
| `true` / `yes`  | `git rev-parse --short HEAD` (honours `core.abbrev`).|
| `"N"` in [1,40] | `git rev-parse --short=N HEAD` (explicit length).    |

Anything else fails config validation at strategy construction with a
clear message. The default remains off so existing configs are not
affected.

## Why this does not weaken ADR 0001

ADR 0001's constraint is *immutability*, not *length*. Short SHAs are
still immutable — they are a deterministic prefix of a commit hash, not a
floating reference like `latest` or `main`. Collision risk exists in
principle but is practically zero at the scale a single-host Compose
deploy operates at: the risk for 7-character abbreviations inside one
repository is the same as for `git log --oneline`, which teams rely on
without incident.

The strategy still refuses empty output, and git itself refuses an
abbreviation that would introduce an ambiguous prefix against the current
refs (it will lengthen the abbreviation automatically). This is stronger
than a naive string truncation.

## Consequences

- Teams whose image builds tag with short SHAs can align their broker
  identity to the same value without a CI rewrite.
- Audit logs and rollback hints become easier to scan.
- Opting in is explicit; no existing `c2quay.yml` changes behaviour.
- `resolved_image_digest` and `manifest_file` are unaffected; they already
  let the operator pick the exact identity.
