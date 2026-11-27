const integer = new Intl.NumberFormat('en-US')
const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 })
const money = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 })
const shortMoney = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2 })
const shortTime = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit', second: '2-digit' })
const hourOnly = new Intl.DateTimeFormat('en-US', { hour: 'numeric', hour12: false })
const hourMinute = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit', hour12: false })
const dayLabel = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric' })
const fullTime = new Intl.DateTimeFormat('en-US', { dateStyle: 'medium', timeStyle: 'medium' })

export const format = {
  count: (value: number) => integer.format(value),
  compact: (value: number) => compact.format(value),
  money: (value: number) => money.format(value),
  /** Two decimals for a headline figure; four stay in tables where cents matter. */
  moneyShort: (value: number) => shortMoney.format(value),
  time: (value: string) => shortTime.format(new Date(value)),
  /** Hour bucket labels follow the reader's clock, matching the offset sent. */
  hour: (value: Date) => hourOnly.format(value),
  hourMinute: (value: Date) => hourMinute.format(value),
  fullTime: (value: string) => fullTime.format(new Date(value)),
  day: (value: Date) => dayLabel.format(value),
  latency: (value: number) => `${integer.format(value)} ms`,
  bytes: (value: number) => (value < 1024 ? `${value} B` : `${compact.format(value / 1024)} KiB`),
  percent: (value: number) => `${value.toFixed(value >= 10 ? 0 : 1)}%`,
  limit: (value: number) => (value ? integer.format(value) : 'Unlimited'),
  budget: (value: number) => (value ? money.format(value) : 'Unlimited'),
}

/**
 * Buckets for the chart, oldest first, so gaps render as zero. They start at the
 * range's own boundary and step forward to the bucket that is running now, which
 * is why the last point is often short: it holds only what has happened so far.
 */
export function bucketsFor(
  rows: { key: string; requests: number; total_tokens: number }[] | null,
  range: { group: 'minute' | 'hour' | 'day'; buckets: number; start: () => Date },
) {
  const byKey = new Map((rows ?? []).map(row => [row.key, row]))
  const from = range.start()
  const stepMs = range.group === 'day' ? 86_400_000 : range.group === 'hour' ? 3_600_000 : 60_000
  return Array.from({ length: range.buckets }, (_, index) => {
    const at = new Date(from.getTime() + index * stepMs)
    const key = range.group === 'day'
      ? localDateKey(at)
      : range.group === 'hour'
        ? `${localDateKey(at)}T${String(at.getHours()).padStart(2, '0')}`
        : `${localDateKey(at)}T${String(at.getHours()).padStart(2, '0')}:${String(at.getMinutes()).padStart(2, '0')}`
    const row = byKey.get(key)
    return {
      label: range.group === 'day' ? format.day(at) : range.group === 'hour' ? format.hour(at) : format.hourMinute(at),
      date: key,
      tokens: row?.total_tokens ?? 0,
      requests: row?.requests ?? 0,
    }
  })
}

/** YYYY-MM-DD on the local clock, matching the offset the query was cut with. */
function localDateKey(at: Date) {
  return `${at.getFullYear()}-${String(at.getMonth() + 1).padStart(2, '0')}-${String(at.getDate()).padStart(2, '0')}`
}

/** A short human label, so a table cell never needs a colour to be read. */
export function statusLabel(status: number) {
  if (status >= 500) return 'Server error'
  if (status >= 400) return 'Client error'
  return 'Success'
}

/** Relative time for lists: "just now", "4m ago", "3h ago", then the date. */
export function since(value: string) {
  const then = new Date(value).getTime()
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (seconds < 45) return 'just now'
  if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`
  if (seconds < 86_400) return `${Math.round(seconds / 3600)}h ago`
  return format.day(new Date(value))
}

