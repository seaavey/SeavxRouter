import { bucketsFor, format } from '@/lib/format'
import type { Usage } from '@/lib/api'
import type { Range } from '@/lib/queries'

/**
 * One series, no legend: the newest bucket is filled and the rest sit in a
 * quiet step so the rhythm reads without a colour key. A 30-day range draws
 * one bar per day, an hourly range one per hour, and the last hour one per
 * minute, so the shape always answers the range the reader picked.
 */
export function VolumeChart({ usage, range }: { usage: Usage; range: Range }) {
  const buckets = bucketsFor(usage.data, range)
  const peak = Math.max(...buckets.map(bucket => bucket.requests), 1)
  const step = Math.max(1, Math.ceil(peak / 4))
  const ceiling = step * 4
  const last = buckets.at(-1)?.date
  const busiest = buckets.reduce((best, bucket) => (bucket.requests > best.requests ? bucket : best), buckets[0])
  // Only every nth label is drawn once the count would otherwise collide.
  const labelEvery = Math.ceil(buckets.length / 8)
  const peakLabel = range.group === 'day' ? 'Busiest day' : range.group === 'hour' ? 'Busiest hour' : 'Busiest minute'

  return (
    <div className="px-6 pb-5">
      <div className="flex gap-4 pt-4">
        <div className="flex h-48 w-10 shrink-0 flex-col justify-between text-right text-xs tabular text-muted-foreground">
          {[4, 3, 2, 1, 0].map(mark => <span key={mark}>{format.compact(mark * step)}</span>)}
        </div>
        <div className="relative min-w-0 flex-1">
          <div aria-hidden className="absolute inset-x-0 top-0 flex h-48 flex-col justify-between">
            {[0, 1, 2, 3, 4].map(line => <i key={line} className="border-t border-border" />)}
          </div>
          <div className="relative flex h-48 items-end gap-px" role="group" aria-label={`Requests per ${range.group}, local time`}>
            {buckets.map(bucket => {
              const measured = Math.max((bucket.requests / ceiling) * 100, bucket.requests ? 3 : 0)
              const current = bucket.date === last
              return (
                <div
                  key={bucket.date}
                  title={`${bucket.label} · ${format.count(bucket.requests)} requests`}
                  aria-label={`${bucket.label}, ${bucket.requests} requests`}
                  className="group/bar relative flex h-full min-w-0 flex-1 items-end justify-center"
                >
                  <div
                    style={{ height: `${measured}%` }}
                    className={`w-full rounded-t-sm transition-colors ${current
                      ? 'bg-foreground'
                      : 'bg-chart-3 group-hover/bar:bg-chart-2'
                      }`}
                  />
                </div>
              )
            })}
          </div>
          <div className="mt-3 flex justify-between text-xs text-muted-foreground">
            {buckets
              .filter((_, index) => index % labelEvery === 0 || index === buckets.length - 1)
              .map(bucket => <span key={bucket.date}>{bucket.label}</span>)}
          </div>
        </div>
      </div>
      <p className="mt-4 ml-14 text-xs text-muted-foreground">
        {peakLabel} was {busiest?.label} with {format.count(busiest?.requests ?? 0)} requests
      </p>
    </div>
  )
}
