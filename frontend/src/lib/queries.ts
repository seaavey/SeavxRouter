import { queryOptions } from '@tanstack/react-query'
import { api, type Model, type ProviderQuota, type RequestLog, type Session, type TenantKey, type Usage } from './api'

/** One place for every query key, so invalidation stays honest. */
export const keys = {
  session: ['session'] as const,
  usage: (range: Range) => ['usage', range] as const,
  logs: (offset: number) => ['logs', offset] as const,
  providers: ['providers'] as const,
  models: ['models'] as const,
  tenantKeys: ['tenant-keys'] as const,
}

export const keyPrefix = { usage: ['usage'] as const, logs: ['logs'] as const }

/**
 * A range is a preset with the bucket its chart draws in and the boundary it
 * starts from. Boundaries are local, not rolling: "Last 24 hours" is today from
 * 00:00 to 00:00, and "Last hour" is the 13:00 to 14:00 block, so a reader
 * comparing two charts is comparing the same clock.
 */
export type Range = {
  label: string
  group: 'minute' | 'hour' | 'day'
  /** Bucket count the chart draws, including the partial current one. */
  buckets: number
  start: () => Date
}

/** Midnight today, local time. */
function localMidnight(daysBack = 0) {
  const at = new Date()
  at.setHours(0, 0, 0, 0)
  at.setDate(at.getDate() - daysBack)
  return at
}

/** The top of the hour this request lands in. */
function currentHour() {
  const at = new Date()
  at.setMinutes(0, 0, 0)
  return at
}

export const ranges: Range[] = [
  { label: 'Last hour', group: 'minute', buckets: 60, start: currentHour },
  { label: 'Last 24 hours', group: 'hour', buckets: 24, start: () => localMidnight() },
  { label: 'Last 7 days', group: 'day', buckets: 7, start: () => localMidnight(6) },
  { label: 'Last 30 days', group: 'day', buckets: 30, start: () => localMidnight(29) },
]

/** Ranges are picked by label in the URL so a link stays readable. */
export function rangeFromParam(value: string | undefined): Range {
  return ranges.find(range => range.label === value) ?? ranges[2]
}

/** Minutes east of UTC, which is what the SQL that cuts buckets expects. */
export function utcOffsetMinutes() {
  return -new Date().getTimezoneOffset()
}

export const sessionQuery = queryOptions({
  queryKey: keys.session,
  queryFn: () => api<Session>('/dashboard/api/session'),
  staleTime: 30_000,
})

export const usageQuery = (range: Range) => {
  const start = range.start()
  const offset = utcOffsetMinutes()
  return queryOptions({
    queryKey: keys.usage(range),
    queryFn: ({ signal }) =>
      api<Usage>(
        `/dashboard/api/usage?group_by=${range.group}&offset=${offset}&start=${encodeURIComponent(start.toISOString())}`,
        { signal },
      ),
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
