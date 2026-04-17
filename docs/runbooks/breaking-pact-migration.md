# Runbook — Breaking Pact migrations

**When this applies:** a provider change (contract path rename, a field
rename that changes the matcher, a protocol swap) that cannot be
verified against a consumer version that is still marked deployed or
released in the broker. `c2quay deploy` blocks on `can-i-deploy` with a
"no verification result" / "verification failed" message naming the
stale pact.

Context: see [ADR 0012](../adr/0012-breaking-pact-migration-runbook.md).
c2quay deliberately does **not** expose a `--skip-gate` / `--force-deploy`
flag. The remediation lives either in the shape of the rollout or in
the broker's state.

## Decision tree

```
                Can the provider support BOTH the old and new
                contract simultaneously in one release?
                                │
               ┌────────────────┴────────────────┐
              yes                               no
               │                                 │
               ▼                                 ▼
   (A) Staged rollout              (B) Broker-side remediation
       (three phases)                   (delete stale pacticipant
                                         version, redeploy)
```

### (A) Staged rollout (default, preferred)

Three phases, each of which passes `can-i-deploy` under normal rules.

- **Phase 1 — dual-contract provider release.** Ship a provider version
  that handles both the old path and the new path. Old contract keeps
  passing against this provider; new contract also passes. `c2quay
  deploy --env production` succeeds; broker records the dual provider.
- **Phase 2 — consumer migration.** Consumers switch to the new path and
  publish new pacts. Each consumer's `c2quay deploy` passes
  independently because the provider supports both paths.
- **Phase 3 — old path removal.** Ship a provider release that drops the
  old path. `c2quay deploy` passes because no deployed consumer still
  depends on it.

This is the shape the Pact community recommends and the shape most
changes can be bent into, even ones that seem like they can't at first.
For a Connect-RPC method rename, phase 1 can be the old method
delegating to the new one internally; for a field rename, phase 1 can
accept both names and canonicalize.

### (B) Broker-side remediation (when dual-contract is impossible)

Generated-RPC method renames are the typical example: the wire-level
method string changes and there is no way to have both in one service
definition.

1. Confirm in the broker UI that the stale consumer pact is genuinely
   obsolete (the consumer has since released a version that uses the
   new path, or is about to in the same rollout window).
2. Delete the stale pacticipant version on the broker:

   ```bash
   pact-broker delete-pacticipant-version \
     --broker-base-url $PACT_BROKER_URL \
     --pacticipant <consumer-name> \
     --version <stale-version>
   ```

   Or via the API:

   ```bash
   curl -X DELETE \
     -u "$PACT_BROKER_USERNAME:$PACT_BROKER_PASSWORD" \
     "$PACT_BROKER_URL/pacticipants/<consumer>/versions/<version>"
   ```

   The broker logs this action. **That log is the audit trail** — do
   not reach for a client-side bypass in c2quay; it would leave a
   weaker trail.
3. Re-run `c2quay deploy --env production`. The gate re-evaluates
   against the new broker state and passes.

## What *not* to do

- **Do not add an ad-hoc `--skip-gate` flag.** See ADR 0012. A client-side
  bypass that the broker does not know about means the next deploy's
  gate will still see the stale state, and the bypass becomes
  load-bearing.
- **Do not retire `DeployedOrReleased` globally on the provider side
  just to unblock one migration.** That selector is the provider's
  guarantee that it is compatible with what is actually in production.
  Turning it off is a much larger compatibility regression than the
  migration you're trying to ship.
- **Do not force-delete a live consumer pact.** Step (B) is only valid
  when the stale version is genuinely obsolete. If the consumer is
  still running that version in prod, you are not doing a migration;
  you are breaking contract with a live consumer and need to coordinate
  before anything else.

## The 2026-04-17 shape (worked example)

- Provider renamed a Connect-RPC method (path change).
- Consumer still had the old method name in its deployed pact.
- `c2quay deploy` blocked the new provider on that pact.
- Dual-contract on the provider was not feasible (the Connect-generated
  service surface doesn't accept two names for one method).
- Remediation: broker-side delete of the stale consumer pacticipant
  version for that pact, then redeploy. Consumer then caught up on the
  new method in its own rollout window.

For future incidents of the same shape, skip straight to (B) and
coordinate the consumer catch-up in the same deploy window.
