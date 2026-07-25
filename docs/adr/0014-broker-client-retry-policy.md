# ADR 0014 — Bounded retry with backoff for the Pact Broker HTTP client

Date: 2026-07-25
Status: Accepted

## Context

`internal/broker/client.go` had no retry logic anywhere. Every call —
fetching the index, `can-i-deploy`, the `all_or_nothing` matrix query,
`record-deployment` — made exactly one HTTP attempt. A single dropped
connection, a broker restart mid-deploy, or a brief 5xx blip from the
broker's load balancer was enough to fail an entire `c2quay deploy` or
`c2quay verify` run, even though the underlying condition was almost always
transient and would have cleared within a second or two.

This matters more for c2quay than for a typical HTTP client because of
*where* the broker sits in the pipeline: a `can-i-deploy` failure caused by
a broker hiccup looks identical, from the operator's side, to a real gate
failure (`ErrGateFailed`) — both stop the deploy. Distinguishing "the
broker said no" from "the broker was briefly unreachable" currently
requires reading logs; it shouldn't require a re-run.

## Decision

`Client.do` (the single choke point every request in this package goes
through) now retries transient failures with bounded exponential backoff:

- **3 attempts total** (1 initial + 2 retries), base delay **500ms**,
  doubling each retry, with up to 50% jitter added on top of each backoff
  step to avoid synchronized retry storms across concurrent callers (the
  `all_or_nothing` gate check plus the worker-pool fan-out in
  `internal/release/pipeline.go` can both be hammering the broker at once).
- **Retried:** network-level errors (DNS, TCP, TLS, connection reset — these
  surface as `ErrBrokerUnreachable`), HTTP `429` (rate limited), and any
  `5xx`.
- **Not retried:** any other `4xx` (`400`, `401`, `403`, `404`, `409`, ...).
  These reflect a problem with the request itself — bad auth, a
  pacticipant/version/environment that doesn't exist, a malformed URL
  template — that repeating will not fix. Burning the retry budget on a
  guaranteed-to-fail request only delays the operator seeing the real
  error.
- **Context cancellation is honored between attempts.** The retry loop
  selects on `ctx.Done()` before each backoff sleep and returns immediately
  if the context is cancelled or its deadline is exceeded, rather than
  sleeping the full backoff out. A context error is never classified as
  retryable, even if the underlying request also happened to fail with a
  network error — cancellation always wins.
- **Per-attempt, not shared, timeout.** Every attempt is a fresh call to
  `c.http.Do`, so `http.Client.Timeout` (30s by default — see `New`) bounds
  each individual attempt. A single slow attempt cannot consume the entire
  retry budget by itself; the caller's own `ctx` deadline (if any) is the
  only thing that bounds the retry loop as a whole.

### GET vs. POST, and why record-deployment is retried

Every GET this client issues (index, `pb:can-i-deploy`, the scoped
can-i-deploy relation, the `all_or_nothing` matrix, `pb:pacticipant-version`,
`pb:environments`) is naturally safe to retry — none of them have side
effects.

The one POST this client ever issues is `pb:record-deployment`
(`internal/broker/deployment.go` — `postJSON` has exactly one caller in the
whole package). Blindly retrying POSTs is usually wrong because most POSTs
are not idempotent — retrying after a timeout risks double-creating
whatever the POST created. `record-deployment` is the documented exception:
per the Pact Broker API, recording the same `(pacticipant, version,
environment)` deployment twice is a no-op on the broker side — it
(re)marks that version as currently deployed to that environment, it does
not create a duplicate deployment record or trigger a duplicate
"undeploy previous version" side effect for an already-applied recording.
That makes it safe to fold into the same blanket retry policy as the GETs,
and it closes a real gap: without this, a `record-deployment` POST that
timed out after the broker had actually applied it left the deploy pipeline
reporting failure (see ADR 0004 / `internal/release/deploy.go`) even though
the broker's state was already correct — an unnecessary alarm with no
recovery path except "trust the logs and don't re-run."

If a second, genuinely non-idempotent POST is ever added to this client,
this blanket "POST inherits the same retry policy as GET" reasoning must be
revisited — e.g. by threading an explicit per-call `retryable` flag through
`do`'s callers instead of relying on "there is only one POST and it happens
to be safe."

### Options.RetryBaseDelay

`broker.Options` gained a `RetryBaseDelay time.Duration` field (default
500ms when unset). It exists primarily so tests don't have to wait out real
backoff delays; production callers should leave it at the default.

## Consequences

- **Good:** a broker restart, a load-balancer blip, or a rate limit window
  no longer fails a deploy or verify outright — it costs at most ~1.5s of
  extra latency (plus jitter) before succeeding on retry.
- **Good:** the record-deployment gap (ADR 0004's "always last" step being
  the least recoverable one to fail) is meaningfully narrower — a transient
  failure there now self-heals instead of requiring an operator to
  manually reconcile broker state.
- **Good:** genuine 4xx failures (bad pacticipant name, expired token,
  broker relation missing) still fail fast — no wasted latency retrying
  something retries cannot fix.
- **Trade-off:** every request through this client can now take up to
  ~1.5s longer in the worst case (persistent 5xx/429) before surfacing the
  final error, versus failing instantly before this change. This is judged
  acceptable given deploy/verify runs are not latency-sensitive at the
  sub-second level.
- **Not addressed here:** retry budgets are per-request, not per-deploy.
  A deploy touching N services in `all_or_nothing: false` mode can still
  multiply retry latency by N if the broker is degraded across the whole
  window. A future ADR could introduce a deploy-wide circuit breaker if
  this proves to matter in practice.
