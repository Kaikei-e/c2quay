# ADR 0012 — Breaking Pact migrations are a runbook, not a tool feature

Date: 2026-04-17
Status: Accepted
Supports invariants from: [ADR 0004](0004-record-deployment-last.md),
[ADR 0009](0009-aggregate-can-i-deploy-for-all-or-nothing.md)

## Context

Part of the 2026-04-17 incident involved renaming a Connect-RPC endpoint on
the provider. The new provider, correctly, could not verify the old
consumer pact that was still tagged as "deployed" in the broker. `c2quay
deploy` blocked the provider release on that failing verification. The
operator could not proceed: c2quay has no flag to bypass the gate, and the
Pact community's historical escape hatch (`--skip-verify`) has been
deprecated and, per the operator's reading, removed from the workflows
they care about. Their phrasing — "--skip-verify 廃止ルート" (the
deprecated route) — names the failure mode we need to decide against.

The temptation is to add `--force-deploy` / `--skip-gate` to c2quay,
possibly with a mandatory reason string for "audit". That would solve the
immediate pain. It would also:

- Weaken the invariant ADR 0004 and ADR 0009 protect: the broker's view
  of deployed versions is the single source of truth for what is safe to
  ship next. A bypass without broker-side acknowledgement silently erodes
  that.
- Recreate, in c2quay, the anti-pattern the Pact community moved away
  from. A client-side audit entry is weaker than a broker-side state
  change, because the next deploy's gate will still see the stale pact
  as "deployed" and block again — unless the bypass flag is also passed,
  which is how audit flags become load-bearing and unavoidable.

## Decision

1. **No `--skip-gate` / `--force-deploy` flag.** c2quay's gate stays
   authoritative. A deploy that fails `can-i-deploy` fails. This is a
   deliberate non-feature; removing it is what a future ADR would have
   to argue for, not this one.

2. **Runbook is the answer.** Two response patterns, documented in
   [`../runbooks/breaking-pact-migration.md`](../runbooks/breaking-pact-migration.md):

   - **Staged rollout (default).** Provider ships a *dual-contract*
     release first: old path and new path both handled. That release
     passes `can-i-deploy` under normal rules. Consumers migrate one at
     a time, each passing `can-i-deploy` independently. Provider
     follows up with a removal release; by that point no deployed
     consumer depends on the old path, so the gate passes.

   - **Broker-side remediation.** When dual-path is not feasible — the
     typical case being a generated-RPC rename where the old method
     simply cannot coexist with the new one under the same service
     definition — the escape hatch lives on the broker, not in c2quay.
     Operators delete the stale pacticipant version in the broker
     (`pact-broker delete-pacticipant-version` or the equivalent API
     call). That action is recorded by the broker itself, which is the
     audit trail we want: next deploy's gate re-evaluates against the
     new broker state and passes.

3. **No config knobs for pact selectors.** c2quay does not expose
   `DeployedOrReleased: false` or `EnablePending: true`. Those are
   provider-verification controls that belong in the provider's pact
   test suite, not in a deployer. Pushing them into c2quay would let a
   breaking migration paper over a missing verification forever.

4. **Deferred, not adopted.** If long-run operational experience shows
   the runbook is insufficient, a future ADR may introduce
   `--acknowledge-breaking-change=<ticket-id>` with: (a) mandatory
   non-empty ticket ref, (b) a structured bypass record under
   `.c2quay/bypasses/<ts>.json`, (c) `record-deployment` still
   invoked so the broker's view stays consistent, (d) an explicit
   broker-side annotation the next gate can see. This ADR deliberately
   does **not** adopt that now; the burden of proof is on the future
   ADR to show the runbook path was genuinely insufficient.

## Consequences

- **Good:** the gate remains something an oncall can trust. When
  `c2quay deploy` says "blocked", it means something.
- **Good:** the runbook path matches the Pact community's own guidance
  (staged rollouts, broker-side state change), so operators learn a
  skill that transfers beyond c2quay.
- **Trade-off:** operators who cannot dual-path eat one manual
  broker-delete per stuck migration. That cost is deliberate; it beats
  a client-side bypass whose audit trail would be weaker.
- **Trade-off:** adding the bypass later (if we ever do) will require
  careful design so it does not regress the ADR 0004/0009 invariants.
  Starting without it keeps the option open in both directions.
- **Not addressed here:** automating the staged rollout itself. That is
  a runbook concern today; a future ADR might codify it as a c2quay
  subcommand if there is demand.
