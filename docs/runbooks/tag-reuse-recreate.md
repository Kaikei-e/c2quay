# Runbook — `--force-recreate` and the tag-reuse problem

**When this applies:** `c2quay deploy` succeeded, the image you intended
to ship is in the local cache, but the container is still the old one.
The diagnostic test is quick: `docker compose ps` shows the container
hasn't been restarted since before the deploy, and `docker compose images`
shows the image in use has a different digest from what `compose.yaml`
resolves to right now.

Context: see [ADR 0011](../adr/0011-force-recreate-escape-hatch.md).

## Root-cause questions (ask these first)

Reaching for `--force-recreate` every deploy is the anti-pattern the
warn-log is there to surface. Before you add the flag, answer:

1. **Did the tag change between builds?** Reusing `app:local` across
   builds is the classic shape of this incident. An immutable tag per
   build (git SHA) makes the digest diff unambiguous and removes this
   failure mode entirely. Prefer this over the flag.
2. **Is `pull_policy` in `compose.yaml` set to anything unusual?**
   `never` or `if_not_present` (old name) can suppress the digest update
   that drives recreation.
3. **Is the image genuinely new?** `docker compose images` shows the
   digest currently in use. Compare against `docker image inspect
   <image>@<tag>` — if they match, the local cache never got the new
   build, and the real fix is a build or pull step, not a flag.

If none of the above explains it, the misfire is Compose's
service-scoped digest diff interacting badly with your setup, and the
flag is the right short-term rescue.

## Use the flag

```bash
c2quay deploy --env production --force-recreate
```

What this does:

- Passes `--force-recreate` through to `docker compose up` for this
  deploy only. Scope is the services in the deploy plan (the explicit
  service list c2quay already constructs).
- Logs a `slog.Warn` record at the time of use; with `--audit-log`
  configured the warning lands in the JSONL audit file.
- Does not change any config. The next `c2quay deploy` without the flag
  reverts to the normal digest-diff behaviour.

What this does *not* do:

- Does not rebuild or repull the image. If the image bytes in the cache
  are wrong, `--force-recreate` still starts a container from those
  wrong bytes. Pair with `deploy.pull: always` or a build step if the
  image itself is the problem.
- Does not force-recreate dependencies outside the plan. If your deploy
  is scoped to `api`, only `api` gets the flag. Broader recreation is a
  separate deploy.

## After the rescue

Open a follow-up:

- Switch the affected service to immutable tags (git SHA, build
  timestamp) if you haven't.
- If the misfire is reproducible with a specific Compose version,
  attach a `docker compose version` output to the issue so we can see
  whether a future `compose` release fixes it and the flag can stop
  being recommended.

Habitual use is the signal; the warn log is how a code reviewer catches
it before it becomes unnoticed policy.
