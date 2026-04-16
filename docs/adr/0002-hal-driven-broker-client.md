# ADR 0002 — HAL-driven broker client

Date: 2026-04-17
Status: Accepted

## Context

The Pact Broker's public API is documented as HAL+JSON, with each resource
carrying a `_links` object that names related resources by relation name. A
naive client can simply hard-code URL paths (`/matrix?...`,
`/pacticipants/{p}/...`) and be done with it. In practice this is fragile:
the broker has already renamed relations once (`pacts` became `pb:pacts`)
and continues to extend its vocabulary.

## Decision

c2quay fetches the index resource in `Client.Start(ctx)` on startup and
caches the `_links` map. Every subsequent operation asks the client for a
relation by name and gets back a `Link{Href, Templated}`. Unknown relations
produce a typed `ErrRelationMissing` error rather than an opaque 404.

URL template expansion is RFC 6570 level-1 only (`{var}`), which is all the
Pact Broker uses today.

## Consequences

- When the broker evolves its routes, c2quay continues to work as long as
  the relation names are stable.
- "Your broker is too old for this feature" becomes a clear startup error
  rather than a mysterious runtime failure deep in a deploy.
- Startup cost is one extra GET to the index resource; negligible.
- Client code is slightly more verbose than hard-coded URLs, but the
  indirection is worth the resilience.
