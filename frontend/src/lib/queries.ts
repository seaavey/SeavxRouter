import { queryOptions } from '@tanstack/react-query'
import { api, type Model, type ProviderQuota, type RequestLog, type Session, type TenantKey, type Usage } from './api'

/** One place for every query key, so invalidation stays honest. */
export const keys = {
  session: ['session'] as const,
  health: ['health'] as const,
  usage: (days: number) => ['usage', days] as const,
  logs: (offset: number) => ['logs', offset] as const,
  providers: ['providers'] as const,
  models: ['models'] as const,
  tenantKeys: ['tenant-keys'] as const,
}

export const keyPrefix = { usage: ['usage'] as const, logs: ['logs'] as const }

export const sessionQuery = queryOptions({
  queryKey: keys.session,
  queryFn: () => api<Session>('/dashboard/api/session'),
  staleTime: 30_000,
})

export const healthQuery = queryOptions({
  queryKey: keys.health,
  queryFn: () => api<{ status: string }>('/healthz'),
  refetchInterval: 15_000,
  retry: false,
})

export const usageQuery = (days: number) => {
  const start = new Date()
  start.setUTCHours(0, 0, 0, 0)
  start.setUTCDate(start.getUTCDate() - (days - 1))
  return queryOptions({
    queryKey: keys.usage(days),
    queryFn: ({ signal }) => api<Usage>(`/dashboard/api/usage?group_by=day&start=${encodeURIComponent(start.toISOString())}`, { signal }),
    staleTime: 15_000,
  })
}

export const logsQuery = (offset: number, limit: number) =>
  queryOptions({
    queryKey: keys.logs(offset),
    queryFn: ({ signal }) => api<{ data: RequestLog[] }>(`/dashboard/api/logging?limit=${limit}&offset=${offset}`, { signal }).then(value => value.data ?? []),
    staleTime: 10_000,
  })

export const providersQuery = queryOptions({
  queryKey: keys.providers,
  queryFn: ({ signal }) => api<{ data: ProviderQuota[] }>('/dashboard/api/quota', { signal }).then(value => value.data ?? []),
  staleTime: 20_000,
})

export const modelsQuery = queryOptions({
  queryKey: keys.models,
  queryFn: ({ signal }) => api<{ data: Model[] }>('/dashboard/api/models', { signal }).then(value => value.data ?? []),
  staleTime: 120_000,
})

export const tenantKeysQuery = queryOptions({
  queryKey: keys.tenantKeys,
  queryFn: ({ signal }) => api<{ keys: TenantKey[] | null }>('/dashboard/api/keys', { signal }).then(value => value.keys ?? []),
  staleTime: 20_000,
})
