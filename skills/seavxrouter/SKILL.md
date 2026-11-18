---
name: seavxrouter
description: Operate, debug, and extend SeavxRouter, a single-binary Go AI gateway exposing upstream providers behind OpenAI Chat Completions, Anthropic Messages, and OpenAI Responses APIs. Use when logging in providers, running or checking the server, changing routing or retries, adding a provider adapter, or working with auth.jsonc, hidden-models.txt, usage.db, pricing, compression, and the /v1 HTTP endpoints.
---

# SeavxRouter

## Overview

SeavxRouter is a Go 1.27 module (`seavxrouter`). `main.go` holds the CLI and
provider wiring; everything else is under `internal/`:

- `backend/` — shared chat types (`BackendAPI`, `ChatRequest`, `ChatDelta`,
  `Usage`), error classification, prompts.
- `completions/` — OpenAI, Anthropic, and Responses wire formats;
  `resolveModel` rejects unknown IDs through `HasModel`.
- `multi/` — prefix routing, combos, and provider selection; one provider per
  request unless the model is `combo/<name>`.
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
  resolve to codex when it lists the model, otherwise antigravity; when only
  custom providers exist, the first custom name (sorted) serves bare IDs.
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
  needs `Check(ctx) []backend.AccountCheck`. Every engine's successful `Chat`
  must set `ChatResult.Account = a.Label()` (`auth.Auth.Label()` returns the
  email, else account ID, else `key:<hash>`), or request logs lose account
  attribution for that provider.
- `combo/<name>` IDs resolve through the combo table (`~/.seavx/combos.jsonc`,
  hot-reloaded by mtime), not through a provider. Chat tries members in order
  and only fails over before any delta was emitted; non-retryable errors end the
  list. `ChatResult.Model/Provider` carry the concrete member that answered, so
  usage and logs bill the real model while the response echoes `combo/<name>`.
  Combo members cannot be other combos.
- Catalog caches should serve the last known list and refresh in the background
  (stale-while-revalidate). A synchronous `/models` fetch adds its full latency
  to the first chat after the TTL expires.

## Common workflows

### Build, test, run

```bash
go build -trimpath -ldflags="-s -w" -o seavxrouter .
go vet ./...
go test ./...
```

Rebuild a binary that is currently running with an atomic rename (avoids
`text file busy`), then restart the supervisor:

```bash
go build -trimpath -ldflags="-s -w" -o seavxrouter.new . && mv seavxrouter.new seavxrouter
systemctl --user restart seavxrouter   # or your own supervisor
curl -s http://127.0.0.1:20128/healthz
```

`./seavxrouter check [provider...]` validates credentials and lists per-account
models; `./seavxrouter tui -once` prints one usage snapshot.

### Run as a background service

`seavxrouter service install [server flags...]` writes
`~/.config/systemd/user/seavxrouter.service` for the current binary, embeds the
flags after `install` (with systemd-safe quoting) plus `WorkingDirectory`, then
runs `systemctl --user daemon-reload`, `enable`, and `restart` (so reinstalling
applies a new binary or flags to an already-active unit). The unit uses
`Restart=on-failure`, `RestartSec=2`, and `WantedBy=default.target`. Manage it
with `service start|stop|restart|status`, follow logs with `service logs`, and
remove it with `service uninstall`; all of it is Linux/systemd-only. The unit is
mode `0600` because it may contain `-api-key`. `loginctl enable-linger $USER`
keeps a user service alive after logout. Installing starts the unit immediately,
so stop a manually started server on the same port first.

### Add or change a provider adapter

1. Implement `NewXEngine(store, auths)` with `Default`, `Models`, `HasModel`,
   `Chat`, `Complete`, `Stream`, `Check`, and `Login`.
2. Wire the provider in `main.go`: `providerNames`, `providerAccounts.list` and
   `count`, `groupAuths`, `newChecker`, and the server startup block.
3. Use `auth.NewAccountCache(store, "<provider>")` so accounts added while the
   server runs are picked up without a restart.
4. Map upstream statuses to `quota:` / `unauthorized:` wrapped errors so account
   rotation classifies them correctly.
5. Add `httptest`-based tests in the adapter package covering stream
   aggregation, tool calls, rotation, and error classification.

### Add a custom OpenAI-compatible provider

`seavxrouter provider add <name> -base-url URL -api-key KEY` registers any
OpenAI-compatible `/v1` endpoint without code changes. Validation is mandatory:
the name must be lowercase `a-z0-9_-`, the base URL must be `http(s)`, and the
key must pass both a non-empty `/models` catalog and a tiny `chat/completions`
smoke test before anything is stored — a public catalog alone proves nothing.
A 401/403 aborts; a quota rejection (402/429) still stores the key with a
`warning:` line. `-model ID` pins the smoke test to one model when the catalog's
first chat models are outside a free plan. Entries land in `auth.jsonc` with
`base_url`; models are exposed as `<name>/<model>`. Manage them with
`provider list` and `provider remove <name>`.

A running server builds custom engines at startup, so restart it after adding or
removing a provider. Adding another key to an existing custom provider reuses
the same base URL and is picked up live by `AccountCache`.

### Add a model combo

A combo is an ordered cross-provider fallback list exposed as `combo/<name>`.
The file lives next to `auth.jsonc` and is re-read on change:

```bash
./seavxrouter combo add fast -models groq/openai/gpt-oss-120b,antigravity/gemini-3.8-flash
./seavxrouter combo list
```

Request `"model": "combo/fast"`. Members are full IDs (bare IDs also resolve);
nested `combo/` members are rejected. Quota, auth, and transient failures before
the first delta move to the next member; a non-retryable error or a partially
streamed answer ends the request. Usage and request logs attribute the member
and provider account that answered. `-combos path` relocates the file for the
server and the CLI.

### Debug a failing or slow request

- `404 model_not_found` — `hidden-models.txt` blocks `HasModel`, or the adapter
  catalog does not list the ID.
- 502 after an extra ~300ms — the error was retryable but not quota, so `multi`
  retried the same provider once before giving up. Check the adapter's status
  mapping.
- Combo 429/502 — every member failed; the server log names each
  `combo member failed` / `combo member unavailable` line in order.
- Slow first call after idle — adapter catalog fetch on the request path;
  prefer stale-while-revalidate.
- Wrong provider/account — inspect `/v1/quota`, `/v1/logging`, and the server
  logs for rotation lines.

## Resources

- `references/providers.md` — provider table, login flows, credential types,
  adapter-specific behavior.
- `references/cli.md` — commands, server and TUI flags, TUI keys, install.
- `references/http-api.md` — endpoints, request examples, routing, attribution.
- `references/configuration.md` — `auth.jsonc`, environment variables, local
  state files, hidden models, usage, pricing, compression, security.
