export interface UsageTotals {
  requests: number
  prompt_tokens: number
  completion_tokens: number
  thinking_tokens: number
  cached_tokens: number
  total_tokens: number
  cost_usd: number
}

export interface Usage {
  totals: UsageTotals
  data: (UsageTotals & { key: string })[] | null
}

export interface RequestLog {
  id: string
  timestamp: string
  endpoint: string
  provider: string
  model: string
  account: string
  user: string
  status: number
  duration_ms: number
  stream: boolean
  /** Failure message, empty when the request succeeded. Bodies are not stored. */
  error?: string
  /** prompt_tokens is net of cached_tokens: they never overlap. */
  usage: { prompt_tokens: number; completion_tokens: number; thinking_tokens: number; cached_tokens: number; total_tokens: number }
}

export interface ProviderAccount {
  provider: string
  email?: string
  account_id?: string
  project_id?: string
  tier?: string
  access_token_expires_at?: string
  status: string
  limited_since?: string
}

export interface ProviderQuota {
  provider: string
  default_model?: string
  accounts: ProviderAccount[] | null
}

export interface Model {
  id: string
  owned_by?: string
}

export interface TenantKey {
  id: number
  name: string
  prefix: string
  created_at: string
  revoked_at?: string
  budget_usd: number
  limit_token: number
}

export interface Session {
  authenticated: boolean
  must_change: boolean
  setup_required?: boolean
}

export interface CreatedKey extends TenantKey {
  key: string
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body) headers.set('Content-Type', 'application/json')
  const response = await fetch(path, { ...options, headers, cache: 'no-store' })
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    if (response.status === 401 && path !== '/dashboard/api/login') {
      window.dispatchEvent(new Event('dashboard-session-expired'))
    }
    throw new Error(body?.error?.message ?? `Request failed (${response.status}).`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
