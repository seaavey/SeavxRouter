import { dayBuckets, format } from '@/lib/format'
import type { Usage } from '@/lib/api'

/**
 * Daily request volume. One series, so one accent: today carries the signal
 * colour and the rest of the week sits in neutral steps.
 */
export function VolumeChart({ usage, days }: { usage: Usage; days: number }) {
  const buckets = dayBuckets(usage.data, days)
  const peak = Math.max(...buckets.map(bucket => bucket.requests), 1)
  const step = Math.max(1, Math.ceil(peak / 4))
  const ceiling = step * 4
  const last = buckets.at(-1)?.date

  return (
    <div className="px-5 pb-4">
      <div className="flex gap-3 pt-2">
        <div className="flex h-44 w-9 shrink-0 flex-col justify-between text-right font-mono text-[11px] tabular text-muted-foreground/80">
          {[4, 3, 2, 1, 0].map(mark => <span key={mark}>{format.compact(mark * step)}</span>)}
        </div>
        <div className="relative min-w-0 flex-1">
          <div aria-hidden className="absolute inset-x-0 top-0 flex h-44 flex-col justify-between">
            {[0, 1, 2, 3, 4].map(line => <i key={line} className="border-t border-dashed border-border" />)}
          </div>
          <div className="relative flex h-44 items-end gap-[2px]" role="group" aria-label={`Daily requests, last ${days} days, UTC`}>
            {buckets.map(bucket => {
              const height = (bucket.requests / ceiling) * 100
              const today = bucket.date === last
              return (
                <div
                  key={bucket.date}
                  title={`${bucket.label} · ${format.count(bucket.requests)} requests`}
                  aria-label={`${bucket.label}, ${bucket.requests} requests`}
                  className="group/bar relative flex h-full min-w-0 flex-1 items-end justify-center"
                >
                  <div
                    style={{ height: `${Math.max(height, bucket.requests ? 2 : 0)}%` }}
                    className={`w-full rounded-t-[2px] transition-colors ${today ? 'bg-signal' : 'bg-chart-2 group-hover/bar:bg-chart-3'}`}
                  />
                </div>
              )
            })}
          </div>
          <div className="mt-2 flex justify-between font-mono text-[11px] text-muted-foreground/80">
            <span>{buckets[0]?.label}</span>
            {days > 7 && <span>{buckets[Math.floor(days / 2)]?.label}</span>}
            <span>{buckets.at(-1)?.label}</span>
          </div>
        </div>
      </div>
    </div>
  )
}
