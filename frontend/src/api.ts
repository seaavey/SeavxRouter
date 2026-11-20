export interface Totals {
  requests: number
  total_tokens: number
  cost_usd: number
}

export interface Usage {
  totals: Totals
  data: (Totals & { key: string })[] | null
}

export interface RequestLog {
  id: string
  timestamp: string
  model: string
  endpoint: string
  provider: string
  status: number
  duration_ms: number
  usage: { total_tokens: number }
}

export interface Provider {
  provider: string
  default_model?: string
  accounts: { email?: string; account_id?: string; status: string }[] | null
}

export interface Model { id: string; owned_by?: string }

export interface TenantKey {
  id: number
  name: string
  prefix: string
  revoked_at?: string
  budget_usd: number
  limit_token: number
}

export async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const headers = new Headers(options?.headers)
  if (options?.body) headers.set('Content-Type', 'application/json')
  const response = await fetch(path, { ...options, headers, cache: 'no-store' })
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    if (response.status === 401 && path !== '/dashboard/api/login') {
      window.dispatchEvent(new Event('dashboard-session-expired'))
    }
    throw new Error(body?.error?.message ?? `Router request failed (${response.status}).`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
