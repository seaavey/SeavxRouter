# HTTP API

All `/v1/*` routes require `Authorization: Bearer <key>` only when the server
was started with `-api-key`. `/healthz` is always unauthenticated.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness probe. |
| `GET` | `/v1/models` | Model catalog across all configured providers. |
| `GET` | `/v1/models/{id}` | One model; provider-prefixed IDs are supported. |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions, streaming supported. |
| `POST` | `/v1/messages` | Anthropic Messages, streaming supported. |
| `POST` | `/v1/responses` | OpenAI Responses, streaming supported. |
| `GET` | `/v1/quota` | Per-account quota for providers that expose it. |
| `GET` | `/v1/usage` | Usage totals and groups. Filters: `provider`, `model`, `user`, `start`, `end`, `group_by`. |
| `GET` | `/v1/compression` | Cumulative tool-output compression statistics. |
| `GET` | `/v1/logging` | Recent request logs with response bodies and token usage. |
| `GET` | `/v1/logging/stream` | SSE stream of finished requests. |
| `GET` | `/v1/logging/{id}` | One request log. |

Attribution for usage and logs comes from `X-Seavx-User` when present, otherwise
a short hash of the bearer token, otherwise `anonymous`.

Model resolution rejects unknown IDs with `404 model_not_found`. Hidden models
are filtered out of `/v1/models` and are not resolvable either.

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
works. When `-api-key` is set, pass that exact value.

## Model routing

Model IDs use `<provider>/<model>`:

```text
codex/gpt-5
antigravity/gemini-3.8-flash
groq/openai/gpt-oss-120b
nvidia/deepseek-ai/deepseek-v4.1-flash
cloudflare/@cf/meta/llama-3.1-8b-instruct
opencode/big-pickle
```

- The first segment selects the provider; the remainder passes through, so IDs
  containing slashes work unchanged.
- Bare IDs resolve to codex when it lists the model, otherwise antigravity.
- Common aliases are normalized (for example `gemini-flash`, dashed versus
  dotted Gemini versions).
- Fallback order on retryable failures: other providers that serve the same
  model, then (quota/auth only) each remaining provider's default model.
