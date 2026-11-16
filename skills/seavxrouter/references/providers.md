# Providers

All provider names below are valid values for `seavxrouter login <provider>`
and prefixes for model IDs in `internal/multi`.

| Provider | Credential | Login | Auto-refresh |
| --- | --- | --- | --- |
| `antigravity` | Google OAuth access + refresh token | `seavxrouter login antigravity` (browser) | Yes |
| `codex` | OpenAI OAuth access + refresh token | `seavxrouter login codex` (browser) | Yes |
| `codebuddy` | Bearer token | `seavxrouter login codebuddy` | No |
| `qoder` | Bearer token | `seavxrouter login qoder [region]` | No |
| `kiro` | AWS device-code tokens | `seavxrouter login kiro [region]` | Yes |
| `verboo` | API key (`vbk_...`) | `seavxrouter login verboo` | No |
| `nvidia` | API key (`nvapi-...`) | `seavxrouter login nvidia` | No |
| `groq` | API key (`gsk_...`) | `seavxrouter login groq` | No |
| `cloudflare` | API token + account ID | `seavxrouter login cloudflare` | No |
| `opencode` | None (local server) | `seavxrouter login opencode` | No |

Browser logins open automatically. API-key logins prompt on the terminal unless
the matching environment variable is set (see
`references/configuration.md`). Storing several entries for one provider adds
them to that provider's round-robin rotation.

## Adapter behavior worth knowing

- **Antigravity** — base URL overridable with `-api-base` (default is the
  built-in Antigravity endpoint). Bare model IDs fall back here.
- **Qoder** — a quota 403 (for example a `pricingUrl` body) does not trigger a
  session refresh. Quota-limited accounts move behind healthy accounts for
  `qoderQuotaCooldown` (10 minutes) but are still tried last, so a request can
  recover once the cooldown expires.
- **Verboo** — free-tier inference can pause with HTTP 503
  `free_tokens_accounting_pending` ("awaiting usage confirmation"). The adapter
  classifies that as quota, so the router rotates accounts or fails over instead
  of retrying the same key. The model catalog is cached for 5 minutes and
  refreshed in the background while stale entries keep serving.
- **NVIDIA, Groq, Cloudflare** — share `providers/upstream`; Cloudflare uses its
  account-scoped paths for model search and chat.
- **OpenCode** — serves only its free tier to requests made inside its own
  runtime, so the adapter proxies through a local `opencode serve` process
  instead of a public endpoint. It starts a private server on demand at
  `127.0.0.1` on a random port with a generated Basic-auth password, keeps state
  in `~/.seavx/opencode`, restarts it if it exits, and stops it on shutdown.
  Conversations are flattened into a single prompt: tool definitions and images
  are not forwarded, and streaming arrives as one delta when generation
  completes. Free usage is rate-limited by OpenCode and subject to its terms.

## Error classification

`backend/errors.go` decides how `multi` reacts:

- `IsQuotaError` matches quota, 429, `resource_exhausted`, `usage_limit`,
  `rate_limit`, billing, `pricingUrl`, and adapter-specific quota wraps.
  Quota skips the same-provider retry.
- `IsAuthError` matches 401/unauthorized/invalid token.
- `IsRetryableError` is quota, auth, or transient transport/5xx wording.

On a quota or auth error, `multi` first tries other configured providers that
list the same model, then gives every remaining provider one attempt with its
own default model. Expect `verboo/<id>` requests to be answered by another
provider when the Verboo key is paused.
