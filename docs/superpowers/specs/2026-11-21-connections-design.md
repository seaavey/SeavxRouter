# Connections directory and provider details

## Intent
Split the crowded connections page into a configured-provider directory and directly addressable provider detail pages.

## Approved design
- `/connections`: one clickable row per configured provider, including custom providers. Show provider name, total accounts, and active account count. Use green text only when the active count is positive; zero stays neutral. Include provider search. Keep providers with no ready accounts visible.
- `/connections/:providerId`: breadcrumb back to Connections, total/active/unavailable counts, account identities and statuses, optional tier and expiry, then searchable provider-owned models with copy-ID actions.
- Active means backend `status === 'ready'`, not a fresh completion probe.
- No quota UI. A future `/qouta` page is separate scope.
- No credential editing, account creation/deletion, secrets, or unconfigured provider catalog.
- Handle loading, API errors, empty results, clipboard errors, and unknown provider IDs.

## Approved model management
- Provider detail supports adding a manual model ID and deleting both discovered and manually added models.
- Before adding or restoring a model, the backend must send a real chat probe to that provider using server-side credentials. For OpenAI-compatible providers, use their `/v1/chat/completions` endpoint. A successful HTTP response alone is insufficient: require a valid chat result. Quota/auth errors, malformed responses, and timeout must not persist or activate the model.
- Show a sanitized upstream error and upstream HTTP status when available. For connection failures and timeouts, show the cause without inventing an upstream status.
- Deletion requires confirmation and removes the model from discovery AND routing: subsequent requests return `model_not_found`. Do not delete the upstream model or provider account.
- Persist additions and deletions across restarts and apply them without requiring an operator restart. Use exact provider-scoped model IDs so deleting one provider's model does not affect another's.
- Secrets remain server-side. Management endpoints require dashboard authentication and existing write-request protections.
- Existing CLI `ProbeKey` is not sufficient for this feature: it accepts quota rejection as successful verification and is a provider onboarding probe, not a strict model validation operation.

## Approach
Use existing TanStack Router and Query, shadcn components, and semantic theme tokens. Split the current combined page into a directory index and a sibling provider detail route. Share pure provider/model selection helpers. Consume the existing provider query for account metadata without rendering quota fields; associate models by the backend's `owned_by`, not guessed ID prefixes.

Keeping everything on one page was rejected because accounts and models make the directory hard to scan. Separate per-account pages are unnecessary for this read-only scope.

## Constraints
Preserve uncommitted request pagination/skeleton work. Do not commit or deploy without permission. No new dependencies are required. Existing production credentials and services remain untouched.
