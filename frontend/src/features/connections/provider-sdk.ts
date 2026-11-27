export type ProviderSDK = 'openai' | 'anthropic'

export const providerSDKs: { value: ProviderSDK; label: string; hint: string }[] = [
  { value: 'openai', label: 'OpenAI compatible', hint: '/v1/models and /v1/chat/completions' },
  { value: 'anthropic', label: 'Anthropic Messages', hint: '/v1/models and /v1/messages' },
]

export function sdkLabel(sdk?: string) {
  return providerSDKs.find(entry => entry.value === sdk)?.label ?? 'OpenAI compatible'
}

/** A provider name becomes the left side of `<name>/<model>`, so it follows the same rules the backend enforces. */
export function validateProviderName(raw: string) {
  const name = raw.trim().toLowerCase()
  if (!name) return 'Name this provider.'
  if (!/^[a-z][a-z0-9_-]{0,31}$/.test(name)) {
    return 'Start with a letter, then a-z, 0-9, _ or - (max 32).'
  }
  return undefined
}

/** A base URL must be an http(s) origin or path, with no query or fragment. */
export function validateBaseURL(raw: string) {
  const value = raw.trim()
  if (!value) return 'Enter the API base URL.'
  let url: URL
  try {
    url = new URL(value)
  } catch {
    return 'Enter a full http:// or https:// URL.'
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return 'Only http and https are supported.'
  if (url.search || url.hash) return 'Remove the query string and fragment.'
  return undefined
}

export function validateAPIKey(raw: string) {
  const value = raw.trim()
  if (!value) return 'Paste the provider API key.'
  if (value.length > 4096) return 'That key is too long.'
  return undefined
}

/** Normalizes a base URL the way the backend stores it: no trailing slash. */
export function normalizeBaseURL(raw: string) {
  return raw.trim().replace(/\/+$/, '')
}
