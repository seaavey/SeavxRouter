# HTTP API

All `/v1/*` routes require `Authorization: Bearer <key>` only when the server
was started with `-api-key`. `/healthz` is always unauthenticated.

With `-api-key` set, two credential kinds are accepted:

- **Master key** — the exact `-api-key` value. Full access, including operator
  routes (`/v1/usage`, `/v1/logging*`, `/v1/quota`, `/v1/compression`) and key
  management (`/v1/keys*`). The TUI and dashboard use it.
- **Tenant key** — a `sk-svr-...` key minted through `/v1/keys` or
  `seavxrouter key create`. Allowed on `/v1/models*` and the three inference
  endpoints only; operator routes return `403 admin_key_required`.

Tenant keys carry an optional monthly USD budget and an optional monthly total
token cap. Inference requests from an exhausted key return
`429 insufficient_quota`; `/v1/models` stays available.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness probe. |
| `GET` | `/v1/models` | Model catalog across all configured providers. |
| `GET` | `/v1/models/{id}` | One model; provider-prefixed IDs are supported. |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions, streaming supported. |
| `POST` | `/v1/messages` | Anthropic Messages, streaming supported. |
| `POST` | `/v1/responses` | OpenAI Responses, streaming supported. |
| `GET` | `/v1/quota` | Per-account quota for providers that expose it. Master only. |
| `GET` | `/v1/usage` | Usage totals and groups. Filters: `provider`, `model`, `user`, `start`, `end`, `group_by`. Master only. |
| `GET` | `/v1/compression` | Cumulative tool-output compression statistics. Master only. |
| `GET` | `/v1/logging` | Recent request logs with response bodies, token usage, and the serving account. Master only. |
| `GET` | `/v1/logging/stream` | SSE stream of finished requests. Master only. |
| `GET` | `/v1/logging/{id}` | One request log. Master only. |
| `POST` | `/v1/keys` | Mint a tenant key. Master only. |
| `GET` | `/v1/keys` | List tenant keys (no plaintext or hashes). Master only. |
| `GET` | `/v1/keys/{id}` | One tenant key. Master only. |
| `PATCH` | `/v1/keys/{id}` | Update `budget_usd` and/or `limit_token`. Master only. |
| `DELETE` | `/v1/keys/{id}` | Revoke a tenant key. Master only. |

Attribution for usage and logs: a tenant key is recorded as `key:<name>`
(overriding `X-Seavx-User`, which cannot be spoofed into another tenant's
budget). Master-key requests use `X-Seavx-User` when present, otherwise a short
hash of the bearer token, otherwise `anonymous`.

Model resolution rejects unknown IDs with `404 model_not_found`. Hidden models
are filtered out of `/v1/models` and are not resolvable either.

Every log entry also carries `account`, the provider credential that served the
request: the account email when known, else the upstream account ID, else a
short fingerprint of the API key.

## Examples

OpenAI Chat Completions:

```bash
curl -N http://127.0.0.1:20128/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "codex/gpt-5",
    "stream": true,
    "messages": [{"role": "user", "content": "Write a haiku about routers."}]
  }'
```

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:20128/v1", api_key="unused")
resp = client.chat.completions.create(
    model="groq/openai/gpt-oss-120b",
    messages=[{"role": "user", "content": "Hello"}],
)
print(resp.choices[0].message.content)
```

Anthropic Messages:

```bash
curl -N http://127.0.0.1:20128/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "antigravity/gemini-3.8-flash",
    "max_tokens": 512,
    "messages": [{"role": "user", "content": "Explain SSE in two sentences."}]
  }'
```

Responses API:

```bash
curl -s http://127.0.0.1:20128/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model": "codex/gpt-5", "input": "Summarize the CAP theorem."}'
```

When auth is disabled, SDKs still require a non-empty `api_key`; any value
works. When `-api-key` is set, pass that exact value (master) or a minted
tenant key.

## Tenant keys and limits

`-api-key` doubles as the admin key for key management. Mint a customer key:

```bash
curl -s -X POST http://127.0.0.1:20128/v1/keys \
  -H "Authorization: Bearer $MASTER" \
  -d '{"name":"acme","budget_usd":50,"limit_token":2000000}'
# {"id":1,"name":"acme","prefix":"sk-svr-1a2b3c4d","budget_usd":50,"limit_token":2000000,"key":"sk-svr-..."}
```

The plaintext `key` is returned once; only its SHA-256 hash is stored. Manage
keys without curl through `seavxrouter key create|list|revoke`.

- `budget_usd` is a monthly USD cap reset on the 1st at 00:00 UTC; `0` means
  unlimited. Spend is summed from the usage ledger for user `key:<name>`, so
  `-usage-db off` disables enforcement.
- `limit_token` is a monthly cap on the key's total tokens as recorded in the
  usage ledger, likewise reset on the 1st at 00:00 UTC; `0` means unlimited.
  Exceeding either limit returns `429 insufficient_quota`.
- Models with no price entry record cost 0 and never consume budget; their
  tokens still count toward `limit_token`.
- Cost and tokens are recorded after a request finishes, so an in-flight request
  can push a key slightly over a limit.
- If the usage ledger cannot be read, capped keys get
  `503 limit_check_failed` instead of being let through: the limits are a
  billing control, so failing open would give uncapped free usage. Keys with no
  caps and master keys never query the ledger and are unaffected. Clients should
  retry after a short backoff rather than treating this as a quota error.
- `DELETE` is a soft revoke: the row and its `revoked_at` stay visible in
  `GET /v1/keys`, and historical usage remains readable.
- Per-customer usage: `GET /v1/usage?user=key:acme` (master key required).

## Model routing

Model IDs use `<provider>/<model>`:

```text
codex/gpt-5
antigravity/gemini-3.8-flash
groq/openai/gpt-oss-120b
nvidia/deepseek-ai/deepseek-v4.1-flash
cloudflare/@cf/meta/llama-3.1-8b-instruct
opencode/big-pickle
combo/fast
```

- The first segment selects the provider; the remainder passes through, so IDs
  containing slashes work unchanged.
- Bare IDs resolve to codex when it lists the model, otherwise antigravity.
- Common aliases are normalized (for example `gemini-flash`, dashed versus
  dotted Gemini versions).
- A request stays on the provider its model names. Retryable failures retry the
  same provider once; quota failures rotate accounts inside the provider.
- `combo/<name>` is the explicit cross-provider fallback. Its members are tried
  in order and the request moves on when a member fails before producing output
  (quota, auth, or transient). A non-retryable failure or a partially streamed
  answer ends the request. Usage and logs attribute the member that answered;
  the response echoes the requested `combo/<name>`.
- Combos are listed by `GET /v1/models` as `combo/<name>` entries with
  `owned_by: "seavx"`, since the router assembles the fallback list itself.
