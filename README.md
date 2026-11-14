# SeavxRouter

**A self-hosted AI gateway that puts multiple providers behind one OpenAI-, Anthropic-, and Responses-compatible API.**

SeavxRouter is a single Go binary that authenticates against the providers you
already use, exposes their models through standard API shapes, rotates across
accounts, tracks token usage and cost, and ships with a live terminal dashboard.

![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-blue)
![Status](https://img.shields.io/badge/status-experimental-orange)

---

> [!WARNING]
> **For educational and research use only.**
>
> SeavxRouter is an independent, unofficial project. It is **not affiliated
> with, endorsed by, or supported by** Google, OpenAI, AWS, CodeBuddy, Qoder,
> Verboo, NVIDIA, Groq, Cloudflare, or any other provider mentioned here.
>
> Using unofficial clients, automated access, or tokens obtained from official
> CLIs **may violate a provider's Terms of Service** and can lead to rate
> limiting, suspension, or permanent account termination. **You are solely
> responsible** for how you use this software, for the accounts you connect,
> and for complying with every applicable agreement and law.
>
> The software is provided **"AS IS", without warranty of any kind**. The
> authors accept **no liability** for account bans, financial loss, data loss,
> service disruption, or legal consequences arising from its use. Do not use it
> to bypass billing, quotas, or access controls, to resell access, or for any
> unlawful purpose.
>
> Read the full [Legal Disclaimer](#legal--disclaimer) before using
> SeavxRouter. If you are a rights holder and want content removed, open an
> issue or contact the maintainer.

---

## Table of contents

- [Features](#features)
- [Supported providers](#supported-providers)
- [Architecture](#architecture)
- [Requirements](#requirements)
- [Quick start](#quick-start)
- [CLI reference](#cli-reference)
- [HTTP API](#http-api)
- [API examples](#api-examples)
- [Model routing](#model-routing)
- [Configuration](#configuration)
- [Operations](#operations)
- [Security notes](#security-notes)
- [Project layout](#project-layout)
- [Contributing](#contributing)
- [License](#license)
- [Legal & Disclaimer](#legal--disclaimer)

## Features

- **Three wire formats** — OpenAI Chat Completions (`/v1/chat/completions`),
  Anthropic Messages (`/v1/messages`), and OpenAI Responses (`/v1/responses`).
- **Nine provider adapters** — Google Antigravity, OpenAI Codex, CodeBuddy,
  Qoder, Kiro, Verboo, NVIDIA NIM, Groq, and Cloudflare Workers AI.
- **Multi-account rotation** — store several accounts per provider; requests are
  balanced round-robin and move to the next account when one runs out of quota.
- **Streaming** — SSE streaming for every supported wire format, including tool
  calls and reasoning deltas.
- **Usage tracking** — optional SQLite ledger with prompt, completion, thinking,
  cached, and total tokens, plus estimated USD cost per request.
- **Cost estimation** — prices loaded from the public models.dev catalog with a
  built-in fallback table and optional per-model overrides.
- **Live TUI dashboard** — grouped usage, recent requests, request detail, and
  an SSE feed of finished calls.
- **Request logging** — the newest 1,000 requests are kept locally with response
  bodies and token counts (request bodies are never persisted).
- **Tool-output compression** — large tool results are trimmed before they are
  forwarded upstream, reducing token cost.
- **Hidden models** — an operator-maintained blocklist removes model IDs from
  discovery without breaking clients that already know them.
- **Optional bearer auth** — protect every `/v1/*` route with a single API key.

## Supported providers

| Provider | Login command | Credential | Auto-refresh |
| --- | --- | --- | --- |
| `antigravity` | `seavxrouter login antigravity` | Google OAuth access + refresh token | Yes |
| `codex` | `seavxrouter login codex` | OpenAI OAuth access + refresh token | Yes |
| `codebuddy` | `seavxrouter login codebuddy` | Bearer token | No |
| `qoder` | `seavxrouter login qoder [region]` | Bearer token | No |
| `kiro` | `seavxrouter login kiro [region]` | AWS device-code tokens | Yes |
| `verboo` | `seavxrouter login verboo` | API key (`vbk_...`) | No |
| `nvidia` | `seavxrouter login nvidia` | API key (`nvapi-...`) | No |
| `groq` | `seavxrouter login groq` | API key (`gsk_...`) | No |
| `cloudflare` | `seavxrouter login cloudflare` | API token + account ID | No |

Login flows that need a browser open one automatically; API-key providers prompt
on the terminal (or read a key from the environment, see
[Configuration](#configuration)). Run `seavxrouter check` afterwards to verify
credentials and list the models each account can see.

## Architecture

```text
 OpenAI SDK / Anthropic SDK / curl / any HTTP client
                         |
                         v
        +----------------------------------+
        |  SeavxRouter  (Go, single binary)|
        |                                  |
        |  /v1/chat/completions            |
        |  /v1/messages                    |   wire-format translation
        |  /v1/responses                   |
        |  /v1/models  /v1/quota  /v1/usage|
        +----------------+-----------------+
                         |
                 +-------+--------+
                 |   MultiBackend |
                 |  route by model|
                 +-------+--------+
                         |
     +-----------+-------+-------+-----------+-----------+
     |           |               |           |           |
 Antigravity   Codex         CodeBuddy     Qoder        Kiro
  (OAuth)     (OAuth)        (bearer)     (bearer)   (device code)
     |           |               |           |           |
     +-----------+-------+-------+-----------+-----------+
                         |
              Verboo / NVIDIA / Groq / Cloudflare
                       (API keys)
```

Each provider adapter owns its authentication, account rotation, model catalog,
and request translation. The `multi` package picks the backend from the model
ID, and the `completions` package converts between wire formats.

## Requirements

- Go **1.27** or newer to build from source.
- Linux, macOS, or Windows.
- Provider accounts and credentials that you are authorized to use.

## Quick start

### 1. Build

```bash
git clone https://github.com/seaavey/SeavxRouter.git
cd SeavxRouter
go build -o seavxrouter .
```

### 2. Log in to at least one provider

```bash
./seavxrouter login antigravity   # opens a browser
./seavxrouter login groq          # prompts for an API key
```

Credentials are written to `~/.seavx/auth.jsonc` with mode `0600`. See
[Configuration](#configuration) for the credential format.

### 3. Verify credentials and models

```bash
./seavxrouter check
```

### 4. Run the router

```bash
./seavxrouter -listen 127.0.0.1:20128
```

Protect the endpoint with an API key whenever it is reachable by anything other
than your own user account:

```bash
API_KEY="$(openssl rand -hex 24)"
./seavxrouter -listen 127.0.0.1:20128 -api-key "$API_KEY"
```

### 5. Send a request

```bash
curl -s http://127.0.0.1:20128/v1/models | jq '.data[0]'

curl -s http://127.0.0.1:20128/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "antigravity/gemini-3.8-flash",
    "messages": [{"role": "user", "content": "Say hello in one sentence."}]
  }' | jq -r '.choices[0].message.content'
```

### 6. Watch usage (optional)

```bash
./seavxrouter tui            # live dashboard, q to quit
./seavxrouter tui -once      # print one snapshot and exit
```

## CLI reference

```text
seavxrouter [flags]              run the HTTP server
seavxrouter login [provider]     authenticate and store credentials
seavxrouter check [provider...]  verify credentials and list models
seavxrouter tui [flags]          terminal usage dashboard
```

### Server flags

| Flag | Default | Description |
| --- | --- | --- |
| `-listen` | `127.0.0.1:20128` | Listen address. Keep it on loopback unless you add `-api-key` and a TLS proxy. |
| `-auth-file` | `~/.seavx/auth.jsonc` | Credentials file. |
| `-api-key` | empty | Require this bearer token on `/v1/*`. Empty disables auth. |
| `-default-model` | empty | Model used when a request omits `model`. |
| `-usage-db` | `~/.seavx/usage.db` | SQLite usage database; `off` disables tracking. |
| `-compress` | `on` | Compress large tool results (`on`/`off`). |
| `-compress-min-bytes` | `2000` | Minimum tool-result size before compression. |
| `-pricing` | empty | JSON price overrides (USD per 1M tokens). |
| `-api-base` | Antigravity API | Antigravity API base URL. |

### TUI flags

| Flag | Default | Description |
| --- | --- | --- |
| `-once` | `false` | Print one snapshot and exit. |
| `-group-by` | `day` | `day`, `hour`, `provider`, `model`, `user`, or `endpoint`. |
| `-refresh` | `2s` | Refresh interval. |
| `-server` | auto | Server URL for the SSE live-log feed; `off` disables it. |
| `-usage-db` | `~/.seavx/usage.db` | Database to read. |

TUI keys: `q`/`Ctrl-C` quit, `j`/`k` or arrows move, `Enter` open detail,
`Esc` back, `r` reload, `p` pause, `1`-`6` switch grouping
(day, provider, model, user, endpoint, hour).

## HTTP API

All `/v1/*` routes require `Authorization: Bearer <key>` **only when** the
server was started with `-api-key`. `/healthz` is always unauthenticated.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness probe. |
| `GET` | `/v1/models` | Model catalog across all configured providers. |
| `GET` | `/v1/models/{id}` | One model, with provider prefix supported. |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions (streaming supported). |
| `POST` | `/v1/messages` | Anthropic Messages (streaming supported). |
| `POST` | `/v1/responses` | OpenAI Responses (streaming supported). |
| `GET` | `/v1/quota` | Per-account quota reported by providers that expose it. |
| `GET` | `/v1/usage` | Usage totals and groups. Filters: `provider`, `model`, `user`, `start`, `end`, `group_by`. |
| `GET` | `/v1/compression` | Cumulative tool-output compression statistics. |
| `GET` | `/v1/logging` | Recent request logs (response bodies and token usage). |
| `GET` | `/v1/logging/stream` | SSE stream of finished requests. |
| `GET` | `/v1/logging/{id}` | One request log. |

Attribution for usage and logs comes from `X-Seavx-User` when present, otherwise
a short hash of the bearer token, otherwise `anonymous`.

## API examples

### OpenAI Chat Completions

```bash
curl -N http://127.0.0.1:20128/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "codex/gpt-5",
    "stream": true,
    "messages": [{"role": "user", "content": "Write a haiku about routers."}]
  }'
```

With the official OpenAI SDK, point `base_url` at the router:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:20128/v1", api_key="unused")
resp = client.chat.completions.create(
    model="groq/openai/gpt-oss-120b",
    messages=[{"role": "user", "content": "Hello"}],
)
print(resp.choices[0].message.content)
```

Set `api_key` to the value passed to `-api-key` when server auth is enabled;
when it is disabled the SDK still requires a non-empty string, and any value
works.

### Anthropic Messages

```bash
curl -N http://127.0.0.1:20128/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "antigravity/gemini-3.8-flash",
    "max_tokens": 512,
    "messages": [{"role": "user", "content": "Explain SSE in two sentences."}]
  }'
```

```python
from anthropic import Anthropic

client = Anthropic(base_url="http://127.0.0.1:20128", api_key="unused")
msg = client.messages.create(
    model="antigravity/gemini-3.8-flash",
    max_tokens=512,
    messages=[{"role": "user", "content": "Hello"}],
)
print(msg.content[0].text)
```

### Responses API

```bash
curl -s http://127.0.0.1:20128/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model": "codex/gpt-5", "input": "Summarize the CAP theorem."}'
```

## Model routing

Model IDs use the form `<provider>/<model>`:

```text
codex/gpt-5
antigravity/gemini-3.8-flash
groq/openai/gpt-oss-120b
nvidia/deepseek-ai/deepseek-v4.1-flash
cloudflare/@cf/meta/llama-3.1-8b-instruct
```

- The first path segment selects the provider; the rest is passed through, so
  model IDs that contain slashes work unchanged.
- Bare IDs (no provider prefix) are resolved by capability: codex when it lists
  the model, otherwise antigravity.
- Common aliases are normalized (for example `gemini-flash` and dashed versus
  dotted Gemini versions).
- `GET /v1/models` reports every model from every account, so clients can
  discover prefixed IDs directly.

## Configuration

### Credentials file

`seavxrouter login` manages `~/.seavx/auth.jsonc`. The file is JSON with `//`
comments allowed. Never commit it and never paste its contents into an issue.

```jsonc
// ~/.seavx/auth.jsonc — generated by `seavxrouter login`; keep private.
[
  {
    "provider": "antigravity",
    "access_token": "REDACTED",
    "refresh_token": "REDACTED",
    "expiry": "2026-11-14T10:03:42+07:00",
    "project_id": "REDACTED",
    "tier": "g1-pro-tier",
    "email": "you@example.com"
  },
  {
    "provider": "groq",
    "access_token": "gsk_REDACTED",
    "refresh_token": "",
    "expiry": "0001-01-01T00:00:00Z"
  },
  {
    "provider": "cloudflare",
    "access_token": "REDACTED",
    "refresh_token": "",
    "expiry": "0001-01-01T00:00:00Z",
    "account_id": "REDACTED"
  }
]
```

Multiple entries for the same provider add accounts to the rotation pool.

### Provider login environment variables

Server configuration is done entirely with CLI flags; run `seavxrouter -h` for
the complete list. These optional variables only save typing during
`seavxrouter login`:

| Variable | Purpose |
| --- | --- |
| `NVIDIA_API_KEY` | Key read by `seavxrouter login nvidia` instead of prompting. |
| `GROQ_API_KEY` | Key read by `seavxrouter login groq` instead of prompting. |
| `CLOUDFLARE_API_TOKEN` | Token read by `seavxrouter login cloudflare`. |
| `CLOUDFLARE_ACCOUNT_ID` | Account ID used by Cloudflare requests and login. |

No `.env` file is loaded automatically. Export the variables in your shell,
your process manager, or your container environment.

### Local state

| Path | Purpose |
| --- | --- |
| `~/.seavx/auth.jsonc` | Provider credentials, mode `0600`. |
| `~/.seavx/usage.db` | SQLite usage ledger and recent request logs. |
| `~/.seavx/hidden-models.txt` | Optional model blocklist (`model-id` or `prefix/` per line, `#` comments). |
| `~/.seavx/cache/models.json` | Cached models.dev pricing catalog. |
| `~/.seavx/server.url` | URL of the running server, used by the TUI for auto-discovery. |

The blocklist lives next to the credentials file and is re-read when it changes.

## Operations

### Usage and cost

Every completed `/v1/*` request can be recorded in SQLite with token counts and
an estimated USD cost. Prices come from the models.dev catalog (cached locally),
falling back to built-in defaults. Override or extend prices with a JSON file:

```json
{
  "my-model": {"input": 0.5, "output": 1.5, "cached": 0.1}
}
```

```bash
./seavxrouter -pricing ./prices.json
```

Prices are USD per one million tokens. Disable tracking with `-usage-db off`.

### Request logs

The newest 1,000 requests are retained with response bodies (up to 10 MB each),
status, latency, provider, model, user, and token usage. Request bodies are
read for routing hints but never persisted. View them through `/v1/logging` or
the TUI; live entries arrive over `/v1/logging/stream`.

### Tool-output compression

Large tool results in incoming requests are compressed before forwarding, which
reduces prompt tokens. Enable or tune with `-compress` and
`-compress-min-bytes`; inspect cumulative savings at `/v1/compression`.

### Quotas

Providers that expose quota report it at `/v1/quota`, aggregated per account.
Quota errors also trigger account rotation and, when another configured
provider serves the same model, a failover attempt.

## Security notes

- The server binds to `127.0.0.1` by default and has **no authentication** until
  you set `-api-key`. Never expose an unauthenticated instance
  to a network.
- Credentials are stored in plaintext in `~/.seavx/auth.jsonc` (mode `0600`) so
  the router can refresh tokens. Protect the host and back up the file securely.
- Response bodies and prompts can contain sensitive data and are written to the
  local usage database. Treat `~/.seavx/usage.db` as sensitive.
- For remote access, terminate TLS at a reverse proxy and require the API key.
- If a credential leaks, revoke it with the provider and run `seavxrouter login`
  again. See [SECURITY.md](SECURITY.md) for reporting vulnerabilities.

## Project layout

```text
.
├── main.go                  CLI entrypoint, flags, provider wiring
├── internal/
│   ├── auth/                Credential store, JSONC parsing, quotas
│   ├── backend/             Shared chat types, errors, prompts, IDs
│   ├── completions/         OpenAI, Anthropic, and Responses wire formats
│   ├── compress/            Tool-output compression engine
│   ├── dashboard/           Terminal usage dashboard
│   ├── httpapi/             Routing, auth, usage, logs, pricing, hidden models
│   ├── multi/               Model-prefix routing and failover
│   ├── providers/           One adapter per upstream provider
│   │   ├── antigravity/     Google Antigravity adapter
│   │   ├── codebuddy/       CodeBuddy adapter
│   │   ├── codex/           OpenAI Codex adapter
│   │   ├── kiro/            Kiro adapter
│   │   ├── qoder/           Qoder adapter
│   │   ├── upstream/        NVIDIA NIM, Groq, Cloudflare Workers AI
│   │   └── verboo/          Verboo adapter
├── docs/                    Design notes and implementation specs
├── CONTRIBUTING.md
├── CODE_OF_CONDUCT.md
├── SECURITY.md
├── DISCLAIMER.md
└── LICENSE
```

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) first. Never include real credentials,
tokens, or personal data in code, tests, logs, or issues.

## License

Released under the [MIT License](LICENSE). Copyright (c) 2026 Seaavey.

## Legal & Disclaimer

This section is part of the project documentation and applies to every use of
SeavxRouter. It supplements, and does not replace, the warranty and liability
terms of the MIT License.

### Educational and research purpose

SeavxRouter is provided strictly for **education, interoperability research,
and personal experimentation**. It exists to study API gateway design, protocol
translation, and multi-account routing. It is not a commercial product, not a
hosted service, and not an official client for any provider.

### No affiliation or endorsement

SeavxRouter is an independent project. It is **not affiliated with, authorized
by, endorsed by, sponsored by, or in any way officially connected to** Google,
OpenAI, Amazon Web Services, CodeBuddy, Qoder, Verboo, NVIDIA, Groq, Cloudflare,
or any other company whose service it can talk to. All product names, logos,
and trademarks are the property of their respective owners and are used only
for identification.

### Your responsibility and provider terms

By using SeavxRouter you acknowledge and agree that:

1. **You are solely responsible** for the accounts, credentials, and devices
   you connect, and for all activity performed through them.
2. **You must comply with every applicable agreement**, including each
   provider's Terms of Service, Acceptable Use Policy, rate limits, and API
   terms, as well as all applicable laws and regulations.
3. Automating access, reusing tokens issued to official clients, or routing
   traffic outside an official client **may breach those agreements** and may
   result in warnings, rate limits, suspension, or permanent account closure.
   **You assume that risk entirely.**
4. You will **not** use this software to bypass payment, quotas, geographic
   restrictions, or access controls; to resell, sublicense, or provide access to
   third parties; to share or trade credentials; to commit fraud; or to gain
   unauthorized access to any system.
5. You will stop using the software and disconnect any affected account if a
   provider asks you to.

### Credentials and privacy

SeavxRouter stores credentials locally in plaintext (mode `0600`) so it can
refresh tokens. It does not operate a backend service and does not transmit
your credentials anywhere except to the provider you configured for the
request. You are responsible for securing the host, the credential file, and
the local usage database. If a credential is exposed, revoke it with the
provider immediately.

### No warranty

The software is provided **"AS IS" and "AS AVAILABLE", without warranty of any
kind**, express or implied, including but not limited to merchantability,
fitness for a particular purpose, title, and non-infringement. Provider APIs
change without notice; features may break, and no maintenance or support
obligation is assumed.

### Limitation of liability

To the maximum extent permitted by law, the authors, maintainers, and
contributors are **not liable for any direct, indirect, incidental, special,
consequential, exemplary, or punitive damages**, including but not limited to
account suspension or termination, loss of data, loss of access, loss of
profits or revenue, service interruption, or legal costs, arising from or
related to the use of or inability to use this software, even if advised of the
possibility of such damages.

### Compliance and takedown

If you are a rights holder and believe this project infringes your rights or
violates your terms, contact the maintainer through the repository. The
maintainer will review and respond in good faith, and may remove or change
content as appropriate.

### Not legal advice

This document is not legal advice. Laws, contracts, and provider policies vary
by jurisdiction and change over time. **Consult a qualified lawyer** before
relying on any part of it.

### Ringkasan (Bahasa Indonesia)

SeavxRouter adalah proyek **edukasi dan riset**, bukan produk resmi dan tidak
terafiliasi dengan provider mana pun. Penggunaan token, otomasi, atau akses di
luar client resmi **dapat melanggar ToS provider** dan berisiko akun dibatasi
atau ditangguhkan — risiko sepenuhnya milik kamu. Jangan gunakan untuk
mengakali biaya/kuota, menjual ulang akses, membagikan kredensial, atau tujuan
ilegal. Perangkat lunak disediakan **"sebagaimana adanya" tanpa jaminan**, dan
penulis tidak bertanggung jawab atas kerugian, banned akun, kehilangan data,
atau konsekuensi hukum apa pun. Dokumen ini bukan nasihat hukum; konsultasikan
dengan ahli hukum jika perlu.
