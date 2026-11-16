---
name: seavxrouter
description: Operate, debug, and extend SeavxRouter, a single-binary Go AI gateway exposing upstream providers behind OpenAI Chat Completions, Anthropic Messages, and OpenAI Responses APIs. Use when logging in providers, running or checking the server, changing routing or failover, adding a provider adapter, or working with auth.jsonc, hidden-models.txt, usage.db, pricing, compression, and the /v1 HTTP endpoints.
---

# SeavxRouter

## Overview

SeavxRouter is a Go 1.27 module (`seavxrouter`). `main.go` holds the CLI and
provider wiring; everything else is under `internal/`:

- `backend/` — shared chat types (`BackendAPI`, `ChatRequest`, `ChatDelta`,
  `Usage`), error classification, prompts.
- `completions/` — OpenAI, Anthropic, and Responses wire formats;
  `resolveModel` rejects unknown IDs through `HasModel`.
- `multi/` — prefix routing, provider fallback, last-resort defaults.
- `auth/` — JSONC credential store, per-provider `AccountCache`, quotas.
- `httpapi/` — handlers, usage SQLite ledger, request logs, pricing,
  hidden-model wrapper.
- `providers/<name>/` — one adapter per upstream.
- `dashboard/` — TUI; `compress/` — tool-output compression.

Single upstream request path: HTTP handler → `completions` translation →
`multi.Chat` → provider engine → upstream; streaming deltas travel back through
the same chain.

## Key invariants

- Model IDs are `<provider>/<model>`; the first segment selects the backend and
  the rest passes through unchanged, so slashes inside model IDs work. Bare IDs
  resolve to codex when it lists the model, otherwise antigravity.
- `ChatDelta.Thinking` carries reasoning deltas and is emitted as
  `reasoning_content` in Responses/Chat streams. Forward reasoning before
  content and treat it as stream start, or clients see silence until the first
  visible token.
- Quota state is keyed by `*auth.Auth`. `AccountCache.Refresh` preserves
  pointers for unchanged credentials; never rebuild entries that did not change.
- Classify per-account allowance failures as quota (`backend.IsQuotaError`) so
  `multi` skips the 300ms same-provider retry and rotates or falls back at once.
- `hiddenBackend` (httpapi) wraps `BackendAPI`. Optional interfaces it must keep
  working (`Provider`, `Quota()`) need explicit forwarding methods.
- Provider engines implement `backend.BackendAPI`; `seavxrouter check` also
  needs `Check(ctx) []backend.AccountCheck`.
- Catalog caches should serve the last known list and refresh in the background
  (stale-while-revalidate). A synchronous `/models` fetch adds its full latency
  to the first chat after the TTL expires.

## Common workflows

### Build, test, run

```bash
go build -trimpath -o seavxrouter .
go vet ./...
go test ./...
```

Rebuild a binary that is currently running with an atomic rename (avoids
`text file busy`), then restart the supervisor:

```bash
go build -trimpath -o seavxrouter.new . && mv seavxrouter.new seavxrouter
systemctl --user restart seavxrouter   # or your own supervisor
curl -s http://127.0.0.1:20128/healthz
```

`./seavxrouter check [provider...]` validates credentials and lists per-account
models; `./seavxrouter tui -once` prints one usage snapshot.

### Add or change a provider adapter

1. Implement `NewXEngine(store, auths)` with `Default`, `Models`, `HasModel`,
   `Chat`, `Complete`, `Stream`, `Check`, and `Login`.
2. Wire the provider in `main.go`: `providerNames`, `providerAccounts.list` and
   `count`, `groupAuths`, `newChecker`, and the server startup block.
3. Use `auth.NewAccountCache(store, "<provider>")` so accounts added while the
   server runs are picked up without a restart.
4. Map upstream statuses to `quota:` / `unauthorized:` wrapped errors so rotation
   and failover classify them correctly.
5. Add `httptest`-based tests in the adapter package covering stream
   aggregation, tool calls, rotation, and error classification.

### Debug a failing or slow request

- `404 model_not_found` — `hidden-models.txt` blocks `HasModel`, or the adapter
  catalog does not list the ID.
- 502 after an extra ~300ms — the error was retryable but not quota, so `multi`
  retried the same provider once before giving up. Check the adapter's status
  mapping.
- Slow first call after idle — adapter catalog fetch on the request path;
  prefer stale-while-revalidate.
- Wrong provider/account — inspect `/v1/quota`, `/v1/logging`, and the server
  logs for rotation and failover lines.

## Resources

- `references/providers.md` — provider table, login flows, credential types,
  adapter-specific behavior.
- `references/cli.md` — commands, server and TUI flags, TUI keys, install.
- `references/http-api.md` — endpoints, request examples, routing, attribution.
- `references/configuration.md` — `auth.jsonc`, environment variables, local
  state files, hidden models, usage, pricing, compression, security.
