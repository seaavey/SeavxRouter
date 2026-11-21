const integer = new Intl.NumberFormat('en-US')
const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 })
const money = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 })
const shortMoney = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2 })
const shortTime = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit', second: '2-digit' })
const dayLabel = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
const fullTime = new Intl.DateTimeFormat('en-US', { dateStyle: 'medium', timeStyle: 'medium' })

export const format = {
  count: (value: number) => integer.format(value),
  compact: (value: number) => compact.format(value),
  money: (value: number) => money.format(value),
  /** Two decimals for a headline figure; four stay in tables where cents matter. */
  moneyShort: (value: number) => shortMoney.format(value),
  time: (value: string) => shortTime.format(new Date(value)),
  fullTime: (value: string) => fullTime.format(new Date(value)),
  day: (value: Date) => dayLabel.format(value),
  latency: (value: number) => `${integer.format(value)} ms`,
  bytes: (value: number) => (value < 1024 ? `${value} B` : `${compact.format(value / 1024)} KiB`),
  percent: (value: number) => `${value.toFixed(value >= 10 ? 0 : 1)}%`,
  limit: (value: number) => (value ? integer.format(value) : 'Unlimited'),
  budget: (value: number) => (value ? money.format(value) : 'Unlimited'),
}

/** UTC day buckets for the chart, oldest first, so gaps render as zero. */
export function dayBuckets(rows: { key: string; requests: number }[] | null, days: number) {
  const byDay = new Map((rows ?? []).map(row => [row.key, row.requests]))
  const end = new Date()
  end.setUTCHours(0, 0, 0, 0)
  return Array.from({ length: days }, (_, index) => {
    const date = new Date(end)
    date.setUTCDate(end.getUTCDate() - (days - 1 - index))
    return { label: format.day(date), date: date.toISOString().slice(0, 10), requests: byDay.get(date.toISOString().slice(0, 10)) ?? 0 }
  })
}

export function statusTone(status: number) {
  if (status >= 500) return 'destructive' as const
  if (status >= 400) return 'warning' as const
  return 'positive' as const
}
