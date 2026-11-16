# SeavxRouter

**A self-hosted AI gateway that puts multiple providers behind one OpenAI-, Anthropic-, and Responses-compatible API.**

SeavxRouter is a single Go binary that authenticates against the providers you
already use, exposes their models through standard API shapes, rotates across
accounts, tracks token usage and cost, and ships with a live terminal dashboard.

[![CI](https://github.com/seaavey/SeavxRouter/actions/workflows/ci.yml/badge.svg)](https://github.com/seaavey/SeavxRouter/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-blue)
![Status](https://img.shields.io/badge/status-experimental-orange)

---

> [!WARNING]
> **For educational and research use only.** SeavxRouter is an independent,
> unofficial project and is not affiliated with any provider. Using unofficial
> clients or tokens obtained from official CLIs may violate a provider's Terms
> of Service and can lead to rate limiting or account termination. You are
> solely responsible for how you use this software. Read
> [DISCLAIMER.md](DISCLAIMER.md) before using it.

---

## Features

- **Three wire formats** — OpenAI Chat Completions, Anthropic Messages, and
  OpenAI Responses, including SSE streaming, tool calls, and reasoning deltas.
- **Ten provider adapters** — browser OAuth, device-code, bearer-token, and
  API-key logins behind one interface.
- **Multi-account rotation** — several accounts per provider, round-robin
  balancing, and quota-aware failover.
- **Usage and cost** — optional SQLite ledger with token counts, estimated USD
  cost from the models.dev catalog, and optional price overrides.
- **Live TUI dashboard** — grouped usage, recent requests, request detail, and
  an SSE feed of finished calls.
- **Request logs** — the newest 1,000 requests with response bodies and token
  counts, never request bodies.
- **Operational controls** — tool-output compression, a hot-reloaded model
  blocklist, and optional bearer auth on `/v1/*`.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/seaavey/SeavxRouter/main/install.sh | sh
```

Pin a release or choose a directory:

```bash
curl -fsSL https://raw.githubusercontent.com/seaavey/SeavxRouter/main/install.sh \
  | sh -s -- --version v0.1.1 --dir "$HOME/bin"
```

Prebuilt static binaries cover Linux/Termux, macOS, Windows, and FreeBSD; see
[Releases](https://github.com/seaavey/SeavxRouter/releases/latest). Go 1.27 or
newer is required to build from source.

## Quick start

```bash
# 1. Log in to at least one provider (browser or API key).
./seavxrouter login opencode      # free models via the local opencode CLI
./seavxrouter login antigravity   # opens a browser
./seavxrouter login groq          # prompts for an API key

# 2. Verify credentials and list each account's models.
./seavxrouter check

# 3. Run the router.
./seavxrouter -listen 127.0.0.1:20128

# 4. Send a request.
curl -s http://127.0.0.1:20128/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "antigravity/gemini-3.8-flash",
    "messages": [{"role": "user", "content": "Say hello in one sentence."}]
  }' | jq -r '.choices[0].message.content'

# 5. Watch usage (optional).
./seavxrouter tui
```

Credentials are written to `~/.seavx/auth.jsonc` with mode `0600`. Protect the
endpoint with `-api-key` whenever it is reachable by anything other than your
own user account.

## Documentation

| Topic | Where |
| --- | --- |
| Full operator and developer reference — providers, CLI, HTTP API, configuration, state, quirks | [`skills/seavxrouter/SKILL.md`](skills/seavxrouter/SKILL.md) |
| Design notes and implementation specs | [`docs/`](docs/) |
| Security policy and vulnerability reporting | [SECURITY.md](SECURITY.md) |
| Contributing and code of conduct | [CONTRIBUTING.md](CONTRIBUTING.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) |
| Legal disclaimer | [DISCLAIMER.md](DISCLAIMER.md) |

## License

Released under the [MIT License](LICENSE). Copyright (c) 2026 Seaavey.
