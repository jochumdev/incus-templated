---
description: The local and end-to-end test tiers, running against a real Incus, logs and coverage.
editor: markdown
published: true
title: Testing
leafwiki_id: 8o5vnjSg6
leafwiki_title: Testing
leafwiki_created_at: "2026-11-15T15:10:00Z"
leafwiki_updated_at: "2026-11-15T15:10:00Z"
leafwiki_creator_id: system
leafwiki_last_author_id: system
---

# Testing

Tests sit next to the code they test, in the same package (`templated`,
`caddy`, `main`). Run them through `just`, which adds `-race`, coverage and a
log; do not call `go test` directly.

## Tiers

| Tier | Command | Needs |
|---|---|---|
| Local | `just test-local [./pkg/...] [-run Name]` | Nothing outside the checkout |
| End to end | `just test-e2e [./pkg/...] [-run Name]` | A reachable Incus remote and `incus-compose` on `PATH` |

`just test-local` sets `TEST_LOCAL=1` and `just test-e2e` sets `TEST_E2E=1`. An
end-to-end test calls `skipE2E(t)` first, which skips it unless `TEST_E2E` is
set and `TEST_LOCAL` is not, so a plain `just test` runs the local tier.

## End to end

The end-to-end tests deploy `test/fixtures/e2e/compose.yaml` with
`incus-compose` into a fresh project, `test-caddy-e2e-<n>`, run incus-templated
in-process against it, and remove the project with `down --project` when the
test ends, pass or fail. Point them at a remote explicitly:

```bash
INCUS_REMOTE=my-remote just test-e2e ./cmd/incus-templated -run TestE2E
```

`TestE2ECaddyReverseProxy` covers the Caddy preset end to end: two
load-balanced upstreams, scaling down and up, and a Caddy restart reading the
file back from its volume. It also runs a second target on the same label with
no commands, writing JSON to the host.

A test that needs an instance to stay up gives it `oci.entrypoint: sh`; it needs
no `sleep`, and the test does not wait for one.

## No mocks

Anything that needs Incus state gets it from a real Incus, in the end-to-end
tier. A fake `incus.InstanceServer` encodes a guess about what the daemon
returns, and a test that passes against the guess proves nothing. See
[CONTRIBUTING.md](https://github.com/jochumdev/incus-caddy-config/blob/main/CONTRIBUTING.md#testing).

Local commands are the one seam: `templated.execCommand` is a package variable
that the `deploy_os` tests replace to check which commands run, and in what
order, without needing `caddy` on the host.

## Logs and coverage

Every run writes `work/logs/<date>.json` (the `gotestsum` event log) and
`work/logs/<date>-cover.out`, and prints per-function and total statement
coverage at the end. `just cover` prints the newest profile again, or the one
given:

```bash
just cover
just cover work/logs/20261115-143344-cover.out
```

`TEST_PROCS` (default `2`) sets `-parallel`, and `TEST_TIMEOUT` (default `10m`)
the overall timeout.
